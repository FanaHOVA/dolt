// Copyright 2019 Liquidata, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// This file incorporates work covered by the following copyright and
// permission notice:
//
// Copyright 2016 Attic Labs, Inc. All rights reserved.
// Licensed under the Apache License, version 2.0:
// http://www.apache.org/licenses/LICENSE-2.0

package nbs

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/dustin/go-humanize"
	"github.com/pkg/errors"

	"github.com/liquidata-inc/dolt/go/store/atomicerr"
	"github.com/liquidata-inc/dolt/go/store/blobstore"
	"github.com/liquidata-inc/dolt/go/store/chunks"
	"github.com/liquidata-inc/dolt/go/store/hash"
)

var ErrFetchFailure = errors.New("fetch failed")

// The root of a Noms Chunk Store is stored in a 'manifest', along with the
// names of the tables that hold all the chunks in the store. The number of
// chunks in each table is also stored in the manifest.

const (
	// StorageVersion is the version of the on-disk Noms Chunks Store data format.
	StorageVersion = "4"

	defaultMemTableSize uint64 = (1 << 20) * 128 // 128MB
	defaultMaxTables           = 256

	defaultIndexCacheSize    = (1 << 20) * 8 // 8MB
	defaultManifestCacheSize = 1 << 23       // 8MB
	preflushChunkCount       = 8
)

var (
	cacheOnce           = sync.Once{}
	globalIndexCache    *indexCache
	makeManifestManager func(manifest) manifestManager
	globalFDCache       *fdCache
)

func makeGlobalCaches() {
	globalIndexCache = newIndexCache(defaultIndexCacheSize)
	globalFDCache = newFDCache(defaultMaxTables)

	manifestCache := newManifestCache(defaultManifestCacheSize)
	manifestLocks := newManifestLocks()
	makeManifestManager = func(m manifest) manifestManager { return manifestManager{m, manifestCache, manifestLocks} }
}

type NomsBlockStore struct {
	mm manifestManager
	p  tablePersister
	c  conjoiner

	mu       sync.RWMutex // protects the following state
	mt       *memTable
	tables   tableSet
	upstream manifestContents

	mtSize   uint64
	putCount uint64

	stats *Stats
}

type Range struct {
	Offset uint64
	Length uint32
}

func (nbs *NomsBlockStore) GetChunkLocations(hashes hash.HashSet) (map[hash.Hash]map[hash.Hash]Range, error) {
	gr := toGetRecords(hashes)

	ranges := make(map[hash.Hash]map[hash.Hash]Range)
	f := func(css chunkSources) error {
		for _, cs := range css {
			switch tr := cs.(type) {
			case *mmapTableReader:
				offsetRecSlice, _ := tr.findOffsets(gr)
				if len(offsetRecSlice) > 0 {
					y, ok := ranges[hash.Hash(tr.h)]

					if !ok {
						y = make(map[hash.Hash]Range)
					}

					for _, offsetRec := range offsetRecSlice {
						ord := offsetRec.ordinal
						length := tr.lengths[ord]
						h := hash.Hash(*offsetRec.a)
						y[h] = Range{Offset: offsetRec.offset, Length: length}

						delete(hashes, h)
					}

					if len(offsetRecSlice) > 0 {
						gr = toGetRecords(hashes)
					}

					ranges[hash.Hash(tr.h)] = y
				}
			case *chunkSourceAdapter:
				y, ok := ranges[hash.Hash(tr.h)]

				if !ok {
					y = make(map[hash.Hash]Range)
				}

				tableIndex, err := tr.index()

				if err != nil {
					return err
				}

				var foundHashes []hash.Hash
				for h := range hashes {
					ord := tableIndex.lookupOrdinal(addr(h))

					if ord < tableIndex.chunkCount {
						foundHashes = append(foundHashes, h)
						y[h] = Range{Offset: tableIndex.offsets[ord], Length: tableIndex.lengths[ord]}
					}
				}

				ranges[hash.Hash(tr.h)] = y

				for _, h := range foundHashes {
					delete(hashes, h)
				}

			default:
				panic(reflect.TypeOf(cs))
			}

		}

		return nil
	}

	err := f(nbs.tables.upstream)

	if err != nil {
		return nil, err
	}

	err = f(nbs.tables.novel)

	if err != nil {
		return nil, err
	}

	return ranges, nil
}

func (nbs *NomsBlockStore) UpdateManifest(ctx context.Context, updates map[hash.Hash]uint32) (mi ManifestInfo, err error) {
	nbs.mm.LockForUpdate()
	defer func() {
		unlockErr := nbs.mm.UnlockForUpdate()

		if err == nil {
			err = unlockErr
		}
	}()

	nbs.mu.Lock()
	defer nbs.mu.Unlock()

	var stats Stats
	var ok bool
	var contents manifestContents
	ok, contents, err = nbs.mm.Fetch(ctx, &stats)

	if err != nil {
		return manifestContents{}, err
	} else if !ok {
		contents = manifestContents{vers: nbs.upstream.vers}
	}

	currSpecs := make(map[addr]bool)
	for _, spec := range contents.specs {
		currSpecs[spec.name] = true
	}

	var addCount int
	for h, count := range updates {
		a := addr(h)

		if _, ok := currSpecs[a]; !ok {
			addCount++
			contents.specs = append(contents.specs, tableSpec{a, count})
		}
	}

	if addCount == 0 {
		return contents, nil
	}

	var updatedContents manifestContents
	updatedContents, err = nbs.mm.Update(ctx, contents.lock, contents, &stats, nil)

	if err != nil {
		return manifestContents{}, err
	}

	nbs.upstream = updatedContents
	nbs.tables, err = nbs.tables.Rebase(ctx, contents.specs, nbs.stats)

	return updatedContents, nil
}

func NewAWSStore(ctx context.Context, nbfVerStr string, table, ns, bucket string, s3 s3svc, ddb ddbsvc, memTableSize uint64) (*NomsBlockStore, error) {
	cacheOnce.Do(makeGlobalCaches)
	readRateLimiter := make(chan struct{}, 32)
	p := &awsTablePersister{
		s3,
		bucket,
		readRateLimiter,
		nil,
		&ddbTableStore{ddb, table, readRateLimiter, nil},
		awsLimits{defaultS3PartSize, minS3PartSize, maxS3PartSize, maxDynamoItemSize, maxDynamoChunks},
		globalIndexCache,
		ns,
	}
	mm := makeManifestManager(newDynamoManifest(table, ns, ddb))
	return newNomsBlockStore(ctx, nbfVerStr, mm, p, inlineConjoiner{defaultMaxTables}, memTableSize)
}

// NewGCSStore returns an nbs implementation backed by a GCSBlobstore
func NewGCSStore(ctx context.Context, nbfVerStr string, bucketName, path string, gcs *storage.Client, memTableSize uint64) (*NomsBlockStore, error) {
	cacheOnce.Do(makeGlobalCaches)

	bucket := gcs.Bucket(bucketName)
	bs := blobstore.NewGCSBlobstore(bucket, path)
	mm := makeManifestManager(blobstoreManifest{"manifest", bs})

	p := &blobstorePersister{bs, s3BlockSize, globalIndexCache}
	return newNomsBlockStore(ctx, nbfVerStr, mm, p, inlineConjoiner{defaultMaxTables}, memTableSize)
}

func NewLocalStore(ctx context.Context, nbfVerStr string, dir string, memTableSize uint64) (*NomsBlockStore, error) {
	cacheOnce.Do(makeGlobalCaches)
	err := checkDir(dir)

	if err != nil {
		return nil, err
	}

	mm := makeManifestManager(fileManifest{dir})
	p := newFSTablePersister(dir, globalFDCache, globalIndexCache)
	return newNomsBlockStore(ctx, nbfVerStr, mm, p, inlineConjoiner{defaultMaxTables}, memTableSize)
}

func checkDir(dir string) error {
	stat, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return fmt.Errorf("path is not a directory: %s", dir)
	}
	return nil
}

func newNomsBlockStore(ctx context.Context, nbfVerStr string, mm manifestManager, p tablePersister, c conjoiner, memTableSize uint64) (*NomsBlockStore, error) {
	if memTableSize == 0 {
		memTableSize = defaultMemTableSize
	}

	nbs := &NomsBlockStore{
		mm:       mm,
		p:        p,
		c:        c,
		tables:   newTableSet(p),
		upstream: manifestContents{vers: nbfVerStr},
		mtSize:   memTableSize,
		stats:    NewStats(),
	}

	t1 := time.Now()
	defer nbs.stats.OpenLatency.SampleTimeSince(t1)

	exists, contents, err := nbs.mm.Fetch(ctx, nbs.stats)

	if err != nil {
		return nil, err
	}

	if exists {
		nbs.upstream = contents
		nbs.tables, err = nbs.tables.Rebase(ctx, contents.specs, nbs.stats)

		if err != nil {
			return nil, err
		}
	}

	return nbs, nil
}

func (nbs *NomsBlockStore) Put(ctx context.Context, c chunks.Chunk) error {
	t1 := time.Now()
	a := addr(c.Hash())
	success := nbs.addChunk(ctx, a, c.Data())

	if !success {
		return errors.New("failed to add chunk")
	}

	nbs.putCount++

	nbs.stats.PutLatency.SampleTimeSince(t1)

	return nil
}

func (nbs *NomsBlockStore) addChunk(ctx context.Context, h addr, data []byte) bool {
	nbs.mu.Lock()
	defer nbs.mu.Unlock()
	if nbs.mt == nil {
		nbs.mt = newMemTable(nbs.mtSize)
	}
	if !nbs.mt.addChunk(h, data) {
		nbs.tables = nbs.tables.Prepend(ctx, nbs.mt, nbs.stats)
		nbs.mt = newMemTable(nbs.mtSize)
		return nbs.mt.addChunk(h, data)
	}
	return true
}

func (nbs *NomsBlockStore) Get(ctx context.Context, h hash.Hash) (chunks.Chunk, error) {
	t1 := time.Now()
	defer func() {
		nbs.stats.GetLatency.SampleTimeSince(t1)
		nbs.stats.ChunksPerGet.Sample(1)
	}()

	a := addr(h)
	data, tables, err := func() ([]byte, chunkReader, error) {
		var data []byte
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()
		if nbs.mt != nil {
			var err error
			data, err = nbs.mt.get(ctx, a, nbs.stats)

			if err != nil {
				return nil, nil, err
			}
		}
		return data, nbs.tables, nil
	}()

	if err != nil {
		return chunks.EmptyChunk, err
	}

	if data != nil {
		return chunks.NewChunkWithHash(h, data), nil
	}

	data, err = tables.get(ctx, a, nbs.stats)

	if err != nil {
		return chunks.EmptyChunk, err
	}

	if data != nil {
		return chunks.NewChunkWithHash(h, data), nil
	}

	return chunks.EmptyChunk, nil
}

func (nbs *NomsBlockStore) GetMany(ctx context.Context, hashes hash.HashSet, foundChunks chan *chunks.Chunk) error {
	t1 := time.Now()
	reqs := toGetRecords(hashes)

	defer func() {
		if len(hashes) > 0 {
			nbs.stats.GetLatency.SampleTimeSince(t1)
			nbs.stats.ChunksPerGet.Sample(uint64(len(reqs)))
		}
	}()

	ae := atomicerr.New()
	wg := &sync.WaitGroup{}

	tables, remaining := func() (tables chunkReader, remaining bool) {
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()
		tables = nbs.tables
		remaining = true
		if nbs.mt != nil {
			remaining = nbs.mt.getMany(ctx, reqs, foundChunks, nil, ae, nbs.stats)
		}

		return
	}()

	if err := ae.Get(); err != nil {
		return err
	}

	if remaining {
		tables.getMany(ctx, reqs, foundChunks, wg, ae, nbs.stats)
		wg.Wait()
	}

	return ae.Get()
}

func toGetRecords(hashes hash.HashSet) []getRecord {
	reqs := make([]getRecord, len(hashes))
	idx := 0
	for h := range hashes {
		a := addr(h)
		reqs[idx] = getRecord{
			a:      &a,
			prefix: a.Prefix(),
		}
		idx++
	}

	sort.Sort(getRecordByPrefix(reqs))
	return reqs
}

func (nbs *NomsBlockStore) CalcReads(hashes hash.HashSet, blockSize uint64) (reads int, split bool, err error) {
	reqs := toGetRecords(hashes)
	tables := func() (tables tableSet) {
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()
		tables = nbs.tables

		return
	}()

	reads, split, remaining, err := tables.calcReads(reqs, blockSize)

	if err != nil {
		return 0, false, err
	}

	if remaining {
		return 0, false, errors.New("failed to find all chunks")
	}

	return
}

func (nbs *NomsBlockStore) Count() (uint32, error) {
	count, tables, err := func() (count uint32, tables chunkReader, err error) {
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()
		if nbs.mt != nil {
			count, err = nbs.mt.count()
		}

		if err != nil {
			return 0, nil, err
		}

		return count, nbs.tables, nil
	}()

	if err != nil {
		return 0, err
	}

	tablesCount, err := tables.count()

	if err != nil {
		return 0, err
	}

	return count + tablesCount, nil
}

func (nbs *NomsBlockStore) Has(ctx context.Context, h hash.Hash) (bool, error) {
	t1 := time.Now()
	defer func() {
		nbs.stats.HasLatency.SampleTimeSince(t1)
		nbs.stats.AddressesPerHas.Sample(1)
	}()

	a := addr(h)
	has, tables, err := func() (bool, chunkReader, error) {
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()

		if nbs.mt != nil {
			has, err := nbs.mt.has(a)

			if err != nil {
				return false, nil, err
			}

			return has, nbs.tables, nil
		}

		return false, nbs.tables, nil
	}()

	if err != nil {
		return false, err
	}

	if !has {
		has, err = tables.has(a)

		if err != nil {
			return false, err
		}
	}

	return has, nil
}

func (nbs *NomsBlockStore) HasMany(ctx context.Context, hashes hash.HashSet) (hash.HashSet, error) {
	t1 := time.Now()

	reqs := toHasRecords(hashes)

	tables, remaining, err := func() (tables chunkReader, remaining bool, err error) {
		nbs.mu.RLock()
		defer nbs.mu.RUnlock()
		tables = nbs.tables

		remaining = true
		if nbs.mt != nil {
			remaining, err = nbs.mt.hasMany(reqs)

			if err != nil {
				return nil, false, err
			}
		}

		return tables, remaining, nil
	}()

	if err != nil {
		return nil, err
	}

	if remaining {
		_, err := tables.hasMany(reqs)

		if err != nil {
			return nil, err
		}
	}

	if len(hashes) > 0 {
		nbs.stats.HasLatency.SampleTimeSince(t1)
		nbs.stats.AddressesPerHas.SampleLen(len(reqs))
	}

	absent := hash.HashSet{}
	for _, r := range reqs {
		if !r.has {
			absent.Insert(hash.New(r.a[:]))
		}
	}
	return absent, nil
}

func toHasRecords(hashes hash.HashSet) []hasRecord {
	reqs := make([]hasRecord, len(hashes))
	idx := 0
	for h := range hashes {
		a := addr(h)
		reqs[idx] = hasRecord{
			a:      &a,
			prefix: a.Prefix(),
			order:  idx,
		}
		idx++
	}

	sort.Sort(hasRecordByPrefix(reqs))
	return reqs
}

func (nbs *NomsBlockStore) Rebase(ctx context.Context) error {
	nbs.mu.Lock()
	defer nbs.mu.Unlock()
	exists, contents, err := nbs.mm.Fetch(ctx, nbs.stats)

	if err != nil {
		return err
	}

	if exists {
		nbs.upstream = contents
		nbs.tables, err = nbs.tables.Rebase(ctx, contents.specs, nbs.stats)

		if err != nil {
			return err
		}
	}

	return nil
}

func (nbs *NomsBlockStore) Root(ctx context.Context) (hash.Hash, error) {
	nbs.mu.RLock()
	defer nbs.mu.RUnlock()
	return nbs.upstream.root, nil
}

func (nbs *NomsBlockStore) Commit(ctx context.Context, current, last hash.Hash) (success bool, err error) {
	t1 := time.Now()
	defer nbs.stats.CommitLatency.SampleTimeSince(t1)

	anyPossiblyNovelChunks := func() bool {
		nbs.mu.Lock()
		defer nbs.mu.Unlock()
		return nbs.mt != nil || nbs.tables.Novel() > 0
	}

	if !anyPossiblyNovelChunks() && current == last {
		err := nbs.Rebase(ctx)

		if err != nil {
			return false, err
		}

		return true, nil
	}

	err = func() error {
		// This is unfortunate. We want to serialize commits to the same store
		// so that we avoid writing a bunch of unreachable small tables which result
		// from optismistic lock failures. However, this means that the time to
		// write tables is included in "commit" time and if all commits are
		// serialized, it means alot more waiting. Allow "non-trivial" tables to be
		// persisted outside of the commit-lock.
		nbs.mu.Lock()
		defer nbs.mu.Unlock()

		if nbs.mt != nil {
			cnt, err := nbs.mt.count()

			if err != nil {
				return err
			}

			if cnt > preflushChunkCount {
				nbs.tables = nbs.tables.Prepend(ctx, nbs.mt, nbs.stats)
				nbs.mt = nil
			}
		}

		return nil
	}()

	if err != nil {
		return false, err
	}

	nbs.mm.LockForUpdate()
	defer func() {
		unlockErr := nbs.mm.UnlockForUpdate()

		if err == nil {
			err = unlockErr
		}
	}()

	for {
		if err := nbs.updateManifest(ctx, current, last); err == nil {
			return true, nil
		} else if err == errOptimisticLockFailedRoot || err == errLastRootMismatch {
			return false, nil
		} else if err != errOptimisticLockFailedTables {
			return false, err
		}

		// I guess this thing infinitely retries without backoff in the case off errOptimisticLockFailedTables
	}
}

var (
	errLastRootMismatch           = fmt.Errorf("last does not match nbs.Root()")
	errOptimisticLockFailedRoot   = fmt.Errorf("root moved")
	errOptimisticLockFailedTables = fmt.Errorf("tables changed")
)

func (nbs *NomsBlockStore) updateManifest(ctx context.Context, current, last hash.Hash) error {
	nbs.mu.Lock()
	defer nbs.mu.Unlock()
	if nbs.upstream.root != last {
		return errLastRootMismatch
	}

	handleOptimisticLockFailure := func(upstream manifestContents) error {
		var err error
		nbs.upstream = upstream
		nbs.tables, err = nbs.tables.Rebase(ctx, upstream.specs, nbs.stats)

		if err != nil {
			return err
		}

		if last != upstream.root {
			return errOptimisticLockFailedRoot
		}

		return errOptimisticLockFailedTables
	}

	if cached, doomed := nbs.mm.updateWillFail(nbs.upstream.lock); doomed {
		// Pre-emptive optimistic lock failure. Someone else in-process moved to the root, the set of tables, or both out from under us.
		return handleOptimisticLockFailure(cached)
	}

	if nbs.mt != nil {
		cnt, err := nbs.mt.count()

		if err != nil {
			return err
		}

		if cnt > 0 {
			nbs.tables = nbs.tables.Prepend(ctx, nbs.mt, nbs.stats)
			nbs.mt = nil
		}
	}

	if nbs.c.ConjoinRequired(nbs.tables) {
		var err error
		nbs.upstream, err = nbs.c.Conjoin(ctx, nbs.upstream, nbs.mm, nbs.p, nbs.stats)

		if err != nil {
			return err
		}

		nbs.tables, err = nbs.tables.Rebase(ctx, nbs.upstream.specs, nbs.stats)

		if err != nil {
			return err
		}

		return errOptimisticLockFailedTables
	}

	specs, err := nbs.tables.ToSpecs()

	if err != nil {
		return err
	}

	newContents := manifestContents{
		vers:  nbs.upstream.vers,
		root:  current,
		lock:  generateLockHash(current, specs),
		specs: specs,
	}

	upstream, err := nbs.mm.Update(ctx, nbs.upstream.lock, newContents, nbs.stats, nil)
	if err != nil {
		return err
	}

	if newContents.lock != upstream.lock {
		// Optimistic lock failure. Someone else moved to the root, the set of tables, or both out from under us.
		return handleOptimisticLockFailure(upstream)
	}

	nbs.upstream = newContents
	nbs.tables, err = nbs.tables.Flatten()

	if err != nil {
		return nil
	}

	return nil
}

func (nbs *NomsBlockStore) Version() string {
	return nbs.upstream.vers
}

func (nbs *NomsBlockStore) Close() (err error) {
	return
}

func (nbs *NomsBlockStore) Stats() interface{} {
	return *nbs.stats
}

func (nbs *NomsBlockStore) StatsSummary() string {
	nbs.mu.Lock()
	defer nbs.mu.Unlock()
	cnt, _ := nbs.tables.count()
	physLen, _ := nbs.tables.physicalLen()
	return fmt.Sprintf("Root: %s; Chunk Count %d; Physical Bytes %s", nbs.upstream.root, cnt, humanize.Bytes(physLen))
}
