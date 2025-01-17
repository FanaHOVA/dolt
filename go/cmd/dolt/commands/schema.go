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
	"strings"

	"github.com/fatih/color"

	"github.com/liquidata-inc/dolt/go/cmd/dolt/cli"
	"github.com/liquidata-inc/dolt/go/cmd/dolt/errhand"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/doltdb"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/env"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/schema"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/schema/alterschema"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/schema/encoding"
	"github.com/liquidata-inc/dolt/go/libraries/doltcore/sql"
	"github.com/liquidata-inc/dolt/go/libraries/utils/argparser"
	"github.com/liquidata-inc/dolt/go/store/types"
)

const (
	exportFlag      = "export"
	defaultParam    = "default"
	tagParam        = "tag"
	notNullFlag     = "not-null"
	addFieldFlag    = "add-column"
	renameFieldFlag = "rename-column"
	dropFieldFlag   = "drop-column"
)

var tblSchemaShortDesc = "Displays and modifies table schemas"
var tblSchemaLongDesc = `dolt table schema displays the schema of tables at a given commit.  If no commit is provided the
working set will be used. 

A list of tables can optionally be provided.  If it is omitted all table schemas will be shown. 

dolt schema --export exports a table's schema into a specified file. Both table and file must be specified.

dolt schema --add-column adds a column to specified table's schema. If no default value is provided the column 
will be empty.

dolt schema --rename-column renames a column of the specified table. 

dolt schema --drop-column removes a column of the specified table.
`

var tblSchemaSynopsis = []string{
	"[<commit>] [<table>...]",
	"--export <table> <file>",
	"--add-column [--default <default_value>] [--not-null] [--tag <tag-number>] <table> <name> <type>",
	"--rename-column <table> <old> <new>",
	"--drop-column <table> <column>",
}

var bold = color.New(color.Bold)

func Schema(commandStr string, args []string, dEnv *env.DoltEnv) int {
	ap := argparser.NewArgParser()
	ap.ArgListHelp["table"] = "table(s) whose schema is being displayed."
	ap.ArgListHelp["commit"] = "commit at which point the schema will be displayed."
	ap.SupportsFlag(exportFlag, "", "exports schema into file.")
	ap.SupportsString(defaultParam, "", "default-value", "If provided all existing rows will be given this value as their default.")
	ap.SupportsUint(tagParam, "", "tag-number", "The numeric tag for the new column.")
	ap.SupportsFlag(notNullFlag, "", "If provided rows without a value in this column will be considered invalid.  If rows already exist and not-null is specified then a default value must be provided.")
	ap.SupportsFlag(addFieldFlag, "", "add columm to table schema.")
	ap.SupportsFlag(renameFieldFlag, "", "rename column for specified table.")
	ap.SupportsFlag(dropFieldFlag, "", "removes column from specified table.")

	help, usage := cli.HelpAndUsagePrinters(commandStr, tblSchemaShortDesc, tblSchemaLongDesc, tblSchemaSynopsis, ap)
	apr := cli.ParseArgs(ap, args, help)
	var root *doltdb.RootValue
	root, _ = GetWorkingWithVErr(dEnv)

	var verr errhand.VerboseError
	if apr.Contains(renameFieldFlag) {
		verr = renameColumn(apr, root, dEnv)
	} else if apr.Contains(addFieldFlag) {
		verr = addField(apr, root, dEnv)
	} else if apr.Contains(exportFlag) {
		verr = exportSchemas(apr, root, dEnv)
	} else if apr.Contains(dropFieldFlag) {
		verr = removeColumn(apr, root, dEnv)
	} else {
		verr = printSchemas(apr, dEnv)
	}

	return HandleVErrAndExitCode(verr, usage)
}

func printSchemas(apr *argparser.ArgParseResults, dEnv *env.DoltEnv) errhand.VerboseError {
	cmStr := "working"
	args := apr.Args()

	var root *doltdb.RootValue
	var verr errhand.VerboseError
	var cm *doltdb.Commit

	if apr.NArg() == 0 {
		cm, verr = nil, nil
	} else {
		cm, verr = MaybeGetCommitWithVErr(dEnv, args[0])
	}

	if verr == nil {
		if cm != nil {
			cmStr = args[0]
			args = args[1:]

			var err error
			root, err = cm.GetRootValue()

			if err != nil {
				verr = errhand.BuildDError("unable to get root value").AddCause(err).Build()
			}
		} else {
			root, verr = GetWorkingWithVErr(dEnv)
		}
	}

	if verr == nil {
		tables := args

		// If the user hasn't specified table names, try to grab them all;
		// show usage and error out if there aren't any
		if len(tables) == 0 {
			var err error
			tables, err = root.GetTableNames(context.TODO())

			if err != nil {
				return errhand.BuildDError("unable to get table names.").AddCause(err).Build()
			}

			if len(tables) == 0 {
				return errhand.BuildDError("").SetPrintUsage().Build()
			}
		}

		var notFound []string
		for _, tblName := range tables {
			tbl, ok, err := root.GetTable(context.TODO(), tblName)

			if err != nil {
				return errhand.BuildDError("unable to get table '%s'", tblName).AddCause(err).Build()
			}

			if !ok {
				notFound = append(notFound, tblName)
			} else {
				verr = printTblSchema(cmStr, tblName, tbl, root)
				cli.Println()
			}
		}

		for _, tblName := range notFound {
			cli.PrintErrln(color.YellowString("%s not found", tblName))
		}
	}

	return verr
}

func printTblSchema(cmStr string, tblName string, tbl *doltdb.Table, root *doltdb.RootValue) errhand.VerboseError {
	cli.Println(bold.Sprint(tblName), "@", cmStr)
	sch, err := tbl.GetSchema(context.TODO())

	if err != nil {
		return errhand.BuildDError("unable to get schema").AddCause(err).Build()
	}

	cli.Println(sql.SchemaAsCreateStmt(tblName, sch))
	return nil
}

func exportSchemas(apr *argparser.ArgParseResults, root *doltdb.RootValue, dEnv *env.DoltEnv) errhand.VerboseError {
	if apr.NArg() != 2 {
		return errhand.BuildDError("Must specify table and file to which table will be exported.").SetPrintUsage().Build()
	}

	tblName := apr.Arg(0)
	fileName := apr.Arg(1)
	root, _ = GetWorkingWithVErr(dEnv)
	if has, err := root.HasTable(context.TODO(), tblName); err != nil {
		return errhand.BuildDError("unable to read from database").AddCause(err).Build()
	} else if !has {
		return errhand.BuildDError(tblName + " not found").Build()
	}

	tbl, _, err := root.GetTable(context.TODO(), tblName)

	if err != nil {
		return errhand.BuildDError("unable to get table").AddCause(err).Build()
	}

	err = exportTblSchema(tbl, fileName, dEnv)
	if err != nil {
		return errhand.BuildDError("file path not valid.").Build()
	}

	return nil
}

func exportTblSchema(tbl *doltdb.Table, filename string, dEnv *env.DoltEnv) errhand.VerboseError {
	sch, err := tbl.GetSchema(context.TODO())

	if err != nil {
		return errhand.BuildDError("error: failed to get schema").AddCause(err).Build()
	}

	jsonSchStr, err := encoding.MarshalAsJson(sch)
	if err != nil {
		return errhand.BuildDError("Failed to encode as json").AddCause(err).Build()
	}

	err = dEnv.FS.WriteFile(filename, []byte(jsonSchStr))
	return errhand.BuildIf(err, "Unable to write "+filename).AddCause(err).Build()
}

func addField(apr *argparser.ArgParseResults, root *doltdb.RootValue, dEnv *env.DoltEnv) errhand.VerboseError {
	if apr.NArg() != 3 {
		return errhand.BuildDError("Must specify table name, column name, column type, and if column required.").SetPrintUsage().Build()
	}

	tblName := apr.Arg(0)
	if has, err := root.HasTable(context.TODO(), tblName); err != nil {
		return errhand.BuildDError("error: could not read tables from database").AddCause(err).Build()
	} else if !has {
		return errhand.BuildDError(tblName + " not found").Build()
	}

	tbl, _, err := root.GetTable(context.TODO(), tblName)

	if err != nil {
		return errhand.BuildDError("error: failed to get table '%s'", tblName).AddCause(err).Build()
	}

	tblSch, err := tbl.GetSchema(context.TODO())
	newFieldName := apr.Arg(1)

	var tag uint64
	if val, ok := apr.GetUint(tagParam); ok {
		tag = val
	} else {
		tag = schema.AutoGenerateTag(tblSch)
	}

	newFieldType := strings.ToLower(apr.Arg(2))
	newFieldKind, ok := schema.LwrStrToKind[newFieldType]
	if !ok {
		return errhand.BuildDError(newFieldType + " is not a valid type for this new column.").SetPrintUsage().Build()
	}

	var defaultVal types.Value
	if val, ok := apr.GetValue(defaultParam); ok {
		if nomsVal, err := doltcore.StringToValue(val, newFieldKind); err != nil {
			return errhand.VerboseErrorFromError(err)
		} else {
			defaultVal = nomsVal
		}
	}

	nullable := alterschema.Null
	if apr.Contains(notNullFlag) {
		nullable = alterschema.NotNull
	}

	newTable, err := alterschema.AddColumnToTable(context.TODO(), dEnv.DoltDB, tbl, tag, newFieldName, newFieldKind, nullable, defaultVal)
	if err != nil {
		return errhand.VerboseErrorFromError(err)
	}

	root, err = root.PutTable(context.Background(), dEnv.DoltDB, tblName, newTable)

	if err != nil {
		return errhand.BuildDError("error: failed to write table back to database").Build()
	}

	return UpdateWorkingWithVErr(dEnv, root)
}

func renameColumn(apr *argparser.ArgParseResults, root *doltdb.RootValue, dEnv *env.DoltEnv) errhand.VerboseError {
	if apr.NArg() != 3 {
		return errhand.BuildDError("Table name, current column name, and new column name are needed to rename column.").SetPrintUsage().Build()
	}

	tblName := apr.Arg(0)
	if has, err := root.HasTable(context.TODO(), tblName); err != nil {
		return errhand.BuildDError("error: failed to read tables from database").AddCause(err).Build()
	} else if !has {
		return errhand.BuildDError(tblName + " not found").Build()
	}

	tbl, _, err := root.GetTable(context.TODO(), tblName)

	if err != nil {
		return errhand.BuildDError("error: failed to get table '%s'", tblName).AddCause(err).Build()
	}

	oldColName := apr.Arg(1)
	newColName := apr.Arg(2)

	newTbl, err := alterschema.RenameColumn(context.Background(), dEnv.DoltDB, tbl, oldColName, newColName)
	if err != nil {
		return errToVerboseErr(oldColName, newColName, err)
	}

	root, err = root.PutTable(context.Background(), dEnv.DoltDB, tblName, newTbl)

	if err != nil {
		return errhand.BuildDError("error: failed to write table back to database").Build()
	}

	return UpdateWorkingWithVErr(dEnv, root)
}

func errToVerboseErr(oldName, newName string, err error) errhand.VerboseError {
	switch err {
	case schema.ErrColNameCollision:
		return errhand.BuildDError("error: A column already exists with the name %s", newName).Build()

	case schema.ErrColNotFound:
		return errhand.BuildDError("error: Column %s unknown", oldName).Build()

	default:
		return errhand.BuildDError("error: Failed to alter schema").AddCause(err).Build()
	}
}

func removeColumn(apr *argparser.ArgParseResults, root *doltdb.RootValue, dEnv *env.DoltEnv) errhand.VerboseError {
	if apr.NArg() != 2 {
		return errhand.BuildDError("Table name and column to be removed must be specified.").SetPrintUsage().Build()
	}

	tblName := apr.Arg(0)
	if has, err := root.HasTable(context.TODO(), tblName); err != nil {
		return errhand.BuildDError("error: failed to read tables from database.").Build()
	} else if !has {
		return errhand.BuildDError(tblName + " not found").Build()
	}

	tbl, _, err := root.GetTable(context.TODO(), tblName)

	if err != nil {
		return errhand.BuildDError("error: failed to get table '%s'", tblName).AddCause(err).Build()
	}

	colName := apr.Arg(1)

	newTbl, err := alterschema.DropColumn(context.Background(), dEnv.DoltDB, tbl, colName)

	if err != nil {
		return errToVerboseErr(colName, "", err)
	}

	root, err = root.PutTable(context.Background(), dEnv.DoltDB, tblName, newTbl)

	if err != nil {
		return errhand.BuildDError("error: failed to write table back to database").AddCause(err).Build()
	}

	return UpdateWorkingWithVErr(dEnv, root)
}
