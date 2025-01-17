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

package commands

import (
	"context"
	"reflect"
	"sort"
	"strconv"

	"github.com/fatih/color"

	"github.com/liquidata-inc/dolt/go/cmd/dolt/cli"
	"github.com/liquidata-inc/dolt/go/cmd/dolt/errhand"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/diff"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/doltdb"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/env"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/env/actions"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/rowconv"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/schema"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/sql"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/table/pipeline"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/table/untyped"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/table/untyped/fwt"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/table/untyped/nullprinter"
	"github.com/liquidata-inc/dolt/go/libraries/utils/argparser"
	"github.com/liquidata-inc/dolt/go/libraries/utils/iohelp"
	"github.com/liquidata-inc/dolt/go/libraries/utils/mathutil"
	"github.com/liquidata-inc/dolt/go/store/hash"
	"github.com/liquidata-inc/dolt/go/store/types"
)

const (
	SchemaOnlyDiff    = 1
	DataOnlyDiff      = 2
	SchemaAndDataDiff = SchemaOnlyDiff | DataOnlyDiff

	DataFlag   = "data"
	SchemaFlag = "schema"
)

var diffShortDesc = "Show changes between commits, commit and working tree, etc"
var diffLongDesc = `Show changes between the working and staged tables, changes between the working tables and the tables within a commit, or changes between tables at two commits.

dolt diff [--options] [<tables>...]
   This form is to view the changes you made relative to the staging area for the next commit. In other words, the differences are what you could tell Dolt to further add but you still haven't. You can stage these changes by using dolt add.

dolt diff [--options] <commit> [<tables>...]
   This form is to view the changes you have in your working tables relative to the named <commit>. You can use HEAD to compare it with the latest commit, or a branch name to compare with the tip of a different branch.

dolt diff [--options] <commit> <commit> [<tables>...]
   This is to view the changes between two arbitrary <commit>.
`

var diffSynopsis = []string{
	"[options] [<commit>] [--data|--schema] [<tables>...]",
	"[options] <commit> <commit> [--data|--schema] [<tables>...]",
}

func Diff(commandStr string, args []string, dEnv *env.DoltEnv) int {
	ap := argparser.NewArgParser()
	ap.SupportsFlag(DataFlag, "d", "Show only the data changes, do not show the schema changes (Both shown by default).")
	ap.SupportsFlag(SchemaFlag, "s", "Show only the schema changes, do not show the data changes (Both shown by default).")
	help, _ := cli.HelpAndUsagePrinters(commandStr, diffShortDesc, diffLongDesc, diffSynopsis, ap)
	apr := cli.ParseArgs(ap, args, help)

	diffParts := SchemaAndDataDiff
	if apr.Contains(DataFlag) && !apr.Contains(SchemaFlag) {
		diffParts = DataOnlyDiff
	} else if apr.Contains(SchemaFlag) && !apr.Contains(DataFlag) {
		diffParts = SchemaOnlyDiff
	}

	r1, r2, tables, verr := getRoots(apr.Args(), dEnv)

	if verr == nil {
		verr = diffRoots(r1, r2, tables, diffParts, dEnv)
	}

	if verr != nil {
		cli.PrintErrln(verr.Verbose())
		return 1
	}

	return 0
}

// this doesnt work correctly.  Need to be able to distinguish commits from tables
func getRoots(args []string, dEnv *env.DoltEnv) (r1, r2 *doltdb.RootValue, tables []string, verr errhand.VerboseError) {
	roots := make([]*doltdb.RootValue, 2)

	i := 0
	for _, arg := range args {
		if cs, err := doltdb.NewCommitSpec(arg, dEnv.RepoState.Head.Ref.String()); err == nil {
			if cm, err := dEnv.DoltDB.Resolve(context.TODO(), cs); err == nil {
				roots[i], err = cm.GetRootValue()

				if err != nil {
					return nil, nil, nil, errhand.BuildDError("error: failed to get root").AddCause(err).Build()
				}

				i++
				continue
			}
		}

		break
	}

	if i < 2 {
		roots[1] = roots[0]
		roots[0], verr = GetWorkingWithVErr(dEnv)

		if verr == nil && i == 0 {
			roots[1], verr = GetStagedWithVErr(dEnv)
		}

		if verr != nil {
			return nil, nil, args, verr
		}
	}

	for ; i < len(args); i++ {
		tbl := args[i]
		has0, err := roots[0].HasTable(context.TODO(), tbl)

		if err != nil {
			return nil, nil, nil, errhand.BuildDError("error: failed to read tables").AddCause(err).Build()
		}

		has1, err := roots[1].HasTable(context.TODO(), tbl)

		if err != nil {
			return nil, nil, nil, errhand.BuildDError("error: failed to read tables").AddCause(err).Build()
		}

		if !(has0 || has1) {
			verr := errhand.BuildDError("error: Unknown table: '%s'", tbl).Build()
			return nil, nil, nil, verr
		}

		tables = append(tables, tbl)
	}

	return roots[0], roots[1], tables, nil
}

func getRootForCommitSpecStr(csStr string, dEnv *env.DoltEnv) (string, *doltdb.RootValue, errhand.VerboseError) {
	cs, err := doltdb.NewCommitSpec(csStr, dEnv.RepoState.Head.Ref.String())

	if err != nil {
		bdr := errhand.BuildDError(`"%s" is not a validly formatted branch, or commit reference.`, csStr)
		return "", nil, bdr.AddCause(err).Build()
	}

	cm, err := dEnv.DoltDB.Resolve(context.TODO(), cs)

	if err != nil {
		return "", nil, errhand.BuildDError(`Unable to resolve "%s"`, csStr).AddCause(err).Build()
	}

	r, err := cm.GetRootValue()

	if err != nil {
		return "", nil, errhand.BuildDError("error: failed to get root").AddCause(err).Build()
	}

	h, err := cm.HashOf()

	if err != nil {
		return "", nil, errhand.BuildDError("error: failed to get commit hash").AddCause(err).Build()
	}

	return h.String(), r, nil
}

func diffRoots(r1, r2 *doltdb.RootValue, tblNames []string, diffParts int, dEnv *env.DoltEnv) errhand.VerboseError {
	var err error
	if len(tblNames) == 0 {
		tblNames, err = actions.AllTables(context.TODO(), r1, r2)
	}

	if err != nil {
		return errhand.BuildDError("error: unable to read tables").AddCause(err).Build()
	}

	for _, tblName := range tblNames {
		tbl1, ok1, err := r1.GetTable(context.TODO(), tblName)

		if err != nil {
			return errhand.BuildDError("error: failed to get table '%s'", tblName).AddCause(err).Build()
		}

		tbl2, ok2, err := r2.GetTable(context.TODO(), tblName)

		if err != nil {
			return errhand.BuildDError("error: failed to get table '%s'", tblName).AddCause(err).Build()
		}

		if !ok1 && !ok2 {
			bdr := errhand.BuildDError("Table could not be found.")
			bdr.AddDetails("The table %s does not exist.", tblName)
			cli.PrintErrln(bdr.Build())
		} else if tbl1 != nil && tbl2 != nil {
			h1, err := tbl1.HashOf()

			if err != nil {
				return errhand.BuildDError("error: failed to get table hash").Build()
			}

			h2, err := tbl2.HashOf()

			if err != nil {
				return errhand.BuildDError("error: failed to get table hash").Build()
			}

			if h1 == h2 {
				continue
			}
		}

		printTableDiffSummary(tblName, tbl1, tbl2)

		if tbl1 == nil || tbl2 == nil {
			continue
		}

		var sch1 schema.Schema
		var sch2 schema.Schema
		var sch1Hash hash.Hash
		var sch2Hash hash.Hash
		rowData1, err := types.NewMap(context.TODO(), dEnv.DoltDB.ValueReadWriter())

		if err != nil {
			return errhand.BuildDError("").AddCause(err).Build()
		}

		rowData2, err := types.NewMap(context.TODO(), dEnv.DoltDB.ValueReadWriter())

		if err != nil {
			return errhand.BuildDError("").AddCause(err).Build()
		}

		if ok1 {
			sch1, err = tbl1.GetSchema(context.TODO())

			if err != nil {
				return errhand.BuildDError("error: failed to get schema").AddCause(err).Build()
			}

			schRef, err := tbl1.GetSchemaRef()

			if err != nil {
				return errhand.BuildDError("error: failed to get schema ref").AddCause(err).Build()
			}

			sch1Hash = schRef.TargetHash()
			rowData1, err = tbl1.GetRowData(context.TODO())

			if err != nil {
				return errhand.BuildDError("error: failed to get row data").AddCause(err).Build()
			}
		}

		if ok2 {
			sch2, err = tbl2.GetSchema(context.TODO())

			if err != nil {
				return errhand.BuildDError("error: failed to get schema").AddCause(err).Build()
			}

			schRef, err := tbl2.GetSchemaRef()

			if err != nil {
				return errhand.BuildDError("error: failed to get schema ref").AddCause(err).Build()
			}

			sch2Hash = schRef.TargetHash()
			rowData2, err = tbl2.GetRowData(context.TODO())

			if err != nil {
				return errhand.BuildDError("error: failed to get row data").AddCause(err).Build()
			}
		}

		var verr errhand.VerboseError

		if diffParts&SchemaOnlyDiff != 0 && sch1Hash != sch2Hash {
			verr = diffSchemas(tblName, sch2, sch1)
		}

		if diffParts&DataOnlyDiff != 0 {
			verr = diffRows(rowData1, rowData2, sch1, sch2)
		}

		if verr != nil {
			return verr
		}
	}

	return nil
}

func diffSchemas(tableName string, sch1 schema.Schema, sch2 schema.Schema) errhand.VerboseError {
	diffs, err := diff.DiffSchemas(sch1, sch2)

	if err != nil {
		return errhand.BuildDError("error: failed to diff schemas").AddCause(err).Build()
	}

	tags := make([]uint64, 0, len(diffs))

	for tag := range diffs {
		tags = append(tags, tag)
	}

	sort.Slice(tags, func(i, j int) bool {
		return tags[i] < tags[j]
	})

	cli.Println("  CREATE TABLE", tableName, "(")

	for _, tag := range tags {
		dff := diffs[tag]
		switch dff.DiffType {
		case diff.SchDiffNone:
			cli.Println(sql.FmtCol(4, 0, 0, *dff.New))
		case diff.SchDiffColAdded:
			cli.Println(color.GreenString("+ " + sql.FmtCol(2, 0, 0, *dff.New)))
		case diff.SchDiffColRemoved:
			// removed from sch2
			cli.Println(color.RedString("- " + sql.FmtCol(2, 0, 0, *dff.Old)))
		case diff.SchDiffColModified:
			// changed in sch2
			n0, t0 := dff.Old.Name, sql.DoltToSQLType[dff.Old.Kind]
			n1, t1 := dff.New.Name, sql.DoltToSQLType[dff.New.Kind]

			nameLen := 0
			typeLen := 0

			if n0 != n1 {
				n0 = color.YellowString(n0)
				n1 = color.YellowString(n1)
				nameLen = mathutil.Max(len(n0), len(n1))
			}

			if t0 != t1 {
				t0 = color.YellowString(t0)
				t1 = color.YellowString(t1)
				typeLen = mathutil.Max(len(t0), len(t1))
			}

			cli.Println("< " + sql.FmtColWithNameAndType(2, nameLen, typeLen, n0, t0, *dff.Old))
			cli.Println("> " + sql.FmtColWithNameAndType(2, nameLen, typeLen, n1, t1, *dff.New))
		}
	}

	cli.Println("  );")
	cli.Println()

	return nil
}

func dumbDownSchema(in schema.Schema) (schema.Schema, error) {
	allCols := in.GetAllCols()

	dumbCols := make([]schema.Column, 0, allCols.Size())
	err := allCols.Iter(func(tag uint64, col schema.Column) (stop bool, err error) {
		col.Name = strconv.FormatUint(tag, 10)
		col.Constraints = nil
		dumbCols = append(dumbCols, col)

		return false, nil
	})

	if err != nil {
		return nil, err
	}

	dumbColColl, _ := schema.NewColCollection(dumbCols...)

	return schema.SchemaFromCols(dumbColColl), nil
}

func diffRows(newRows, oldRows types.Map, newSch, oldSch schema.Schema) errhand.VerboseError {
	dumbNewSch, err := dumbDownSchema(newSch)

	if err != nil {
		return errhand.BuildDError("").AddCause(err).Build()
	}

	dumbOldSch, err := dumbDownSchema(oldSch)

	if err != nil {
		return errhand.BuildDError("").AddCause(err).Build()
	}

	untypedUnionSch, err := untyped.UntypedSchemaUnion(dumbNewSch, dumbOldSch)

	if err != nil {
		return errhand.BuildDError("Failed to merge schemas").Build()
	}

	newToUnionConv := rowconv.IdentityConverter
	if newSch != nil {
		newToUnionMapping, err := rowconv.TagMapping(newSch, untypedUnionSch)

		if err != nil {
			return errhand.BuildDError("Error creating unioned mapping").AddCause(err).Build()
		}

		newToUnionConv, _ = rowconv.NewRowConverter(newToUnionMapping)
	}

	oldToUnionConv := rowconv.IdentityConverter
	if oldSch != nil {
		oldToUnionMapping, err := rowconv.TagMapping(oldSch, untypedUnionSch)

		if err != nil {
			return errhand.BuildDError("Error creating unioned mapping").AddCause(err).Build()
		}

		oldToUnionConv, _ = rowconv.NewRowConverter(oldToUnionMapping)
	}

	ad := diff.NewAsyncDiffer(1024)
	ad.Start(context.TODO(), newRows, oldRows)
	defer ad.Close()

	src := diff.NewRowDiffSource(ad, oldToUnionConv, newToUnionConv, untypedUnionSch)
	defer src.Close()

	oldColNames := make(map[uint64]string)
	newColNames := make(map[uint64]string)
	err = untypedUnionSch.GetAllCols().Iter(func(tag uint64, col schema.Column) (stop bool, err error) {
		oldCol, oldOk := oldSch.GetAllCols().GetByTag(tag)
		newCol, newOk := newSch.GetAllCols().GetByTag(tag)

		if oldOk {
			oldColNames[tag] = oldCol.Name
		} else {
			oldColNames[tag] = ""
		}

		if newOk {
			newColNames[tag] = newCol.Name
		} else {
			newColNames[tag] = ""
		}

		return false, nil
	})

	if err != nil {
		return errhand.BuildDError("error: failed to map columns to tags").Build()
	}

	schemasEqual := reflect.DeepEqual(oldColNames, newColNames)
	numHeaderRows := 1
	if !schemasEqual {
		numHeaderRows = 2
	}

	sink, err := diff.NewColorDiffSink(iohelp.NopWrCloser(cli.CliOut), untypedUnionSch, numHeaderRows)

	if err != nil {
		return errhand.BuildDError("").AddCause(err).Build()
	}

	defer sink.Close()

	fwtTr := fwt.NewAutoSizingFWTTransformer(untypedUnionSch, fwt.HashFillWhenTooLong, 1000)
	nullPrinter := nullprinter.NewNullPrinter(untypedUnionSch)
	transforms := pipeline.NewTransformCollection(
		pipeline.NewNamedTransform(nullprinter.NULL_PRINTING_STAGE, nullPrinter.ProcessRow),
		pipeline.NamedTransform{Name: fwtStageName, Func: fwtTr.TransformToFWT},
	)

	var verr errhand.VerboseError
	badRowCallback := func(trf *pipeline.TransformRowFailure) (quit bool) {
		verr = errhand.BuildDError("Failed transforming row").AddDetails(trf.TransformName).AddDetails(trf.Details).Build()
		return true
	}

	sinkProcFunc := pipeline.ProcFuncForSinkFunc(sink.ProcRowWithProps)
	p := pipeline.NewAsyncPipeline(pipeline.ProcFuncForSourceFunc(src.NextDiff), sinkProcFunc, transforms, badRowCallback)

	if schemasEqual {
		schRow, err := untyped.NewRowFromTaggedStrings(newRows.Format(), untypedUnionSch, newColNames)

		if err != nil {

		}

		p.InjectRow(fwtStageName, schRow)
	} else {
		newSchRow, err := untyped.NewRowFromTaggedStrings(newRows.Format(), untypedUnionSch, oldColNames)

		if err != nil {

		}

		p.InjectRowWithProps(fwtStageName, newSchRow, map[string]interface{}{diff.DiffTypeProp: diff.DiffModifiedOld})
		oldSchRow, err := untyped.NewRowFromTaggedStrings(newRows.Format(), untypedUnionSch, newColNames)

		if err != nil {

		}

		p.InjectRowWithProps(fwtStageName, oldSchRow, map[string]interface{}{diff.DiffTypeProp: diff.DiffModifiedNew})
	}

	p.Start()
	if err = p.Wait(); err != nil {
		return errhand.BuildDError("Error diffing: %v", err.Error()).Build()
	}

	return verr
}

var emptyHash = hash.Hash{}

func printTableDiffSummary(tblName string, tbl1, tbl2 *doltdb.Table) {
	bold := color.New(color.Bold)

	bold.Printf("diff --dolt a/%[1]s b/%[1]s\n", tblName)

	if tbl1 == nil {
		bold.Println("deleted table")
	} else if tbl2 == nil {
		bold.Println("added table")
	} else {
		h1, err := tbl1.HashOf()

		if err != nil {
			panic(err)
		}

		bold.Printf("--- a/%s @ %s\n", tblName, h1.String())

		h2, err := tbl2.HashOf()

		if err != nil {
			panic(err)
		}

		bold.Printf("+++ b/%s @ %s\n", tblName, h2.String())
	}
}
