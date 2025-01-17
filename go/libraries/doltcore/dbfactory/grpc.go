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

package dbfactory

import (
	"context"
	"fmt"
	"net/url"

	"google.golang.org/grpc"

	remotesapi "github.com/liquidata-inc/dolt/go/gen/proto/dolt/services/remotesapi_v1alpha1"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/remotestorage"
	"github.com/liquidata-inc/dolt/go/store/chunks"
	"github.com/liquidata-inc/dolt/go/store/datas"
	"github.com/liquidata-inc/dolt/go/store/types"
)

// GRPCConnectionProvider is an interface for getting a *grpc.ClientConn.
type GRPCConnectionProvider interface {
	GrpcConn(hostAndPort string, insecure bool) (*grpc.ClientConn, error)
}

// DoldRemoteFactory is a DBFactory implementation for creating databases backed by a remote server that implements the
// GRPC rpcs defined by remoteapis.ChunkStoreServiceClient
type DoltRemoteFactory struct {
	grpcCP   GRPCConnectionProvider
	insecure bool
}

// NewDoltRemoteFactory creates a DoltRemoteFactory instance using the given GRPCConnectionProvider, and insecure setting
func NewDoltRemoteFactory(grpcCP GRPCConnectionProvider, insecure bool) DoltRemoteFactory {
	return DoltRemoteFactory{grpcCP, insecure}
}

// CreateDB creates a database backed by a remote server that implements the GRPC rpcs defined by
// remoteapis.ChunkStoreServiceClient
func (fact DoltRemoteFactory) CreateDB(ctx context.Context, nbf *types.NomsBinFormat, urlObj *url.URL, params map[string]string) (datas.Database, error) {
	var db datas.Database

	cs, err := fact.newChunkStore(ctx, nbf, urlObj, params)

	if err != nil {
		return nil, err
	}

	db = datas.NewDatabase(cs)

	return db, err
}

func (fact DoltRemoteFactory) newChunkStore(ctx context.Context, nbf *types.NomsBinFormat, urlObj *url.URL, params map[string]string) (chunks.ChunkStore, error) {
	conn, err := fact.grpcCP.GrpcConn(urlObj.Host, fact.insecure)

	if err != nil {
		return nil, err
	}

	csClient := remotesapi.NewChunkStoreServiceClient(conn)
	cs, err := remotestorage.NewDoltChunkStoreFromPath(ctx, nbf, urlObj.Path, urlObj.Host, csClient)

	if err == remotestorage.ErrInvalidDoltSpecPath {
		return nil, fmt.Errorf("invalid dolt url '%s'", urlObj.String())
	}

	return cs, err
}
