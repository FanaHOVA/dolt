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

package rowconv

import (
	"fmt"

	"github.com/liquidata-inc/dolt/go/libraries/doltcore"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/row"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/schema"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/table/pipeline"
	"github.com/liquidata-inc/dolt/go/store/types"
)

var IdentityConverter = &RowConverter{nil, true, nil}

// RowConverter converts rows from one schema to another
type RowConverter struct {
	// FieldMapping is a mapping from source column to destination column
	*FieldMapping
	// IdentityConverter is a bool which is true if the converter is doing nothing.
	IdentityConverter bool
	ConvFuncs         map[uint64]doltcore.ConvFunc
}

func newIdentityConverter(mapping *FieldMapping) *RowConverter {
	return &RowConverter{mapping, true, nil}
}

// NewRowConverter creates a a row converter from a given FieldMapping.
func NewRowConverter(mapping *FieldMapping) (*RowConverter, error) {
	if nec, err := isNecessary(mapping.SrcSch, mapping.DestSch, mapping.SrcToDest); err != nil {
		return nil, err
	} else if !nec {
		return newIdentityConverter(mapping), nil
	}

	convFuncs := make(map[uint64]doltcore.ConvFunc, len(mapping.SrcToDest))
	for srcTag, destTag := range mapping.SrcToDest {
		destCol, destOk := mapping.DestSch.GetAllCols().GetByTag(destTag)
		srcCol, srcOk := mapping.SrcSch.GetAllCols().GetByTag(srcTag)

		if !destOk || !srcOk {
			return nil, fmt.Errorf("Could not find column being mapped. src tag: %d, dest tag: %d", srcTag, destTag)
		}

		convFuncs[srcTag] = doltcore.GetConvFunc(srcCol.Kind, destCol.Kind)

		if convFuncs[srcTag] == nil {
			return nil, fmt.Errorf("Unsupported conversion from type %s to %s", srcCol.KindString(), destCol.KindString())
		}
	}

	return &RowConverter{mapping, false, convFuncs}, nil
}

// Convert takes a row maps its columns to their destination columns, and performs any type conversion needed to create
// a row of the expected destination schema.
func (rc *RowConverter) Convert(inRow row.Row) (row.Row, error) {
	if rc.IdentityConverter {
		return inRow, nil
	}

	outTaggedVals := make(row.TaggedValues, len(rc.SrcToDest))
	_, err := inRow.IterCols(func(tag uint64, val types.Value) (stop bool, err error) {
		convFunc, ok := rc.ConvFuncs[tag]

		if ok {
			outTag := rc.SrcToDest[tag]
			outVal, err := convFunc(val)

			if err != nil {
				return false, err
			}

			outTaggedVals[outTag] = outVal
		}

		return false, nil
	})

	if err != nil {
		return nil, err
	}

	return row.New(inRow.Format(), rc.DestSch, outTaggedVals)
}

func isNecessary(srcSch, destSch schema.Schema, destToSrc map[uint64]uint64) (bool, error) {
	srcCols := srcSch.GetAllCols()
	destCols := destSch.GetAllCols()

	if len(destToSrc) != srcCols.Size() || len(destToSrc) != destCols.Size() {
		return true, nil
	}

	for k, v := range destToSrc {
		if k != v {
			return true, nil
		}

		srcCol, srcOk := srcCols.GetByTag(v)
		destCol, destOk := destCols.GetByTag(k)

		if !srcOk || !destOk {
			panic("There is a bug.  FieldMapping creation should prevent this from happening")
		}

		if srcCol.IsPartOfPK != destCol.IsPartOfPK {
			return true, nil
		}

		if srcCol.Kind != destCol.Kind {
			return true, nil
		}
	}

	srcPKCols := srcSch.GetPKCols()
	destPKCols := destSch.GetPKCols()

	if srcPKCols.Size() != destPKCols.Size() {
		return true, nil
	}

	i := 0
	err := destPKCols.Iter(func(tag uint64, col schema.Column) (stop bool, err error) {
		srcPKCol := srcPKCols.GetByIndex(i)

		if srcPKCol.Tag != col.Tag {
			return true, nil
		}

		i++
		return false, nil
	})

	if err != nil {
		return false, err
	}

	return false, nil
}

// GetRowConvTranformFunc can be used to wrap a RowConverter and use that RowConverter in a pipeline.
func GetRowConvTransformFunc(rc *RowConverter) func(row.Row, pipeline.ReadableMap) ([]*pipeline.TransformedRowResult, string) {
	if rc.IdentityConverter {
		return func(inRow row.Row, props pipeline.ReadableMap) (outRows []*pipeline.TransformedRowResult, badRowDetails string) {
			return []*pipeline.TransformedRowResult{{RowData: inRow, PropertyUpdates: nil}}, ""
		}
	} else {
		return func(inRow row.Row, props pipeline.ReadableMap) (outRows []*pipeline.TransformedRowResult, badRowDetails string) {
			outRow, err := rc.Convert(inRow)

			if err != nil {
				return nil, err.Error()
			}

			if isv, err := row.IsValid(outRow, rc.DestSch); err != nil {
				return nil, err.Error()
			} else if !isv {
				col, err := row.GetInvalidCol(outRow, rc.DestSch)

				if err != nil {
					return nil, "invalid column"
				} else {
					return nil, "invalid column: " + col.Name
				}
			}

			return []*pipeline.TransformedRowResult{{RowData: outRow, PropertyUpdates: nil}}, ""
		}
	}
}
