package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dbharbor/backend/internal/driver"
	"dbharbor/backend/internal/driver/sqlbase"
)

type rebuildCol struct {
	def     driver.ColumnDef
	srcExpr string
}

func (d *SQLiteDriver) needsTableRebuild(ctx context.Context, req driver.AlterTableRequest) bool {
	var st *driver.TableStructure
	loadStruct := func() *driver.TableStructure {
		if st == nil {
			s, err := d.GetTableStructure(ctx, req.Database, "", req.Table)
			if err == nil {
				st = &s
			}
		}
		return st
	}

	for _, act := range req.Actions {
		switch strings.ToUpper(strings.TrimSpace(act.Action)) {
		case "MODIFY_COLUMN", "ADD_FOREIGN_KEY", "DROP_FOREIGN_KEY":
			return true
		case "ADD_COLUMN":
			if act.Column.IsPrimaryKey || strings.TrimSpace(act.AfterColumn) != "" {
				return true
			}
		case "DROP_COLUMN":
			colName := act.Column.Name
			if colName == "" {
				colName = act.OldName
			}
			if s := loadStruct(); s != nil {
				for _, c := range s.Columns {
					if strings.EqualFold(c.Name, colName) && c.IsPrimaryKey {
						return true
					}
				}
				for _, idx := range s.Indexes {
					for _, ic := range idx.Columns {
						if strings.EqualFold(ic, colName) {
							return true
						}
					}
				}
				for _, fk := range s.ForeignKeys {
					for _, fc := range fk.Columns {
						if strings.EqualFold(fc, colName) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func defaultExprForSQLiteColumn(col driver.ColumnDef) string {
	if strings.TrimSpace(col.DefaultValue) != "" {
		return col.DefaultValue
	}
	if col.Nullable && !col.IsPrimaryKey {
		return "NULL"
	}
	upper := strings.ToUpper(col.Type)
	if strings.Contains(upper, "INT") || strings.Contains(upper, "REAL") || strings.Contains(upper, "FLOA") ||
		strings.Contains(upper, "DOUB") || strings.Contains(upper, "NUM") || strings.Contains(upper, "DEC") || strings.Contains(upper, "BOOL") {
		return "0"
	}
	return "''"
}

func (d *SQLiteDriver) alterTableWithRebuild(ctx context.Context, req driver.AlterTableRequest) ([]string, error) {
	if strings.TrimSpace(req.Table) == "" {
		return nil, errors.New("table name cannot be empty")
	}
	st, err := d.GetTableStructure(ctx, req.Database, "", req.Table)
	if err != nil {
		return nil, fmt.Errorf("failed to read current table structure: %w", err)
	}
	if len(st.Columns) == 0 {
		return nil, fmt.Errorf("table %s not found or has no columns", req.Table)
	}

	cols := make([]rebuildCol, 0, len(st.Columns)+2)
	for _, c := range st.Columns {
		cols = append(cols, rebuildCol{
			def:     c,
			srcExpr: sqlbase.QuoteIdent(sqlbase.DialectSQLite, c.Name),
		})
	}

	indexes := make([]driver.IndexDef, 0, len(st.Indexes))
	for _, idx := range st.Indexes {
		if idx.Primary || strings.HasPrefix(strings.ToLower(idx.Name), "sqlite_autoindex_") {
			continue
		}
		cpCols := append([]string(nil), idx.Columns...)
		indexes = append(indexes, driver.IndexDef{
			Name:      idx.Name,
			Columns:   cpCols,
			Unique:    idx.Unique,
			IndexType: idx.IndexType,
		})
	}

	fks := make([]driver.ForeignKeyDef, 0, len(st.ForeignKeys))
	for _, fk := range st.ForeignKeys {
		fks = append(fks, driver.ForeignKeyDef{
			Name:       fk.Name,
			Columns:    append([]string(nil), fk.Columns...),
			RefTable:   fk.RefTable,
			RefColumns: append([]string(nil), fk.RefColumns...),
			OnUpdate:   fk.OnUpdate,
			OnDelete:   fk.OnDelete,
		})
	}

	finalTable := req.Table

	renameRefs := func(oldCol, newCol string) {
		if strings.EqualFold(oldCol, newCol) && oldCol == newCol {
			return
		}
		for i := range indexes {
			for j := range indexes[i].Columns {
				if strings.EqualFold(indexes[i].Columns[j], oldCol) {
					indexes[i].Columns[j] = newCol
				}
			}
		}
		for i := range fks {
			for j := range fks[i].Columns {
				if strings.EqualFold(fks[i].Columns[j], oldCol) {
					fks[i].Columns[j] = newCol
				}
			}
		}
	}

	for _, act := range req.Actions {
		switch strings.ToUpper(strings.TrimSpace(act.Action)) {
		case "ADD_COLUMN":
			col := act.Column
			if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
				return nil, errors.New("column name and type cannot be empty when adding a column")
			}
			for _, existing := range cols {
				if strings.EqualFold(existing.def.Name, col.Name) {
					return nil, fmt.Errorf("column %s already exists", col.Name)
				}
			}
			rc := rebuildCol{
				def:     col,
				srcExpr: defaultExprForSQLiteColumn(col),
			}
			inserted := false
			if after := strings.TrimSpace(act.AfterColumn); after != "" {
				for idx, existing := range cols {
					if strings.EqualFold(existing.def.Name, after) {
						cols = append(cols[:idx+1], append([]rebuildCol{rc}, cols[idx+1:]...)...)
						inserted = true
						break
					}
				}
			}
			if !inserted {
				cols = append(cols, rc)
			}

		case "MODIFY_COLUMN":
			col := act.Column
			if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.Type) == "" {
				return nil, errors.New("column name and type cannot be empty when modifying a column")
			}
			target := strings.TrimSpace(act.OldName)
			if target == "" {
				target = col.Name
			}
			foundIdx := -1
			for i, existing := range cols {
				if strings.EqualFold(existing.def.Name, target) {
					foundIdx = i
					break
				}
			}
			if foundIdx < 0 {
				return nil, fmt.Errorf("column to modify not found: %s", target)
			}

			prev := cols[foundIdx]
			expr := prev.srcExpr
			if !strings.EqualFold(strings.TrimSpace(prev.def.Type), strings.TrimSpace(col.Type)) {
				expr = fmt.Sprintf("CAST(%s AS %s)", expr, col.Type)
			}
			if (!col.Nullable || col.IsPrimaryKey) && prev.def.Nullable {
				expr = fmt.Sprintf("COALESCE(%s, %s)", expr, defaultExprForSQLiteColumn(col))
			}

			cols[foundIdx] = rebuildCol{
				def:     col,
				srcExpr: expr,
			}
			renameRefs(target, col.Name)

			if after := strings.TrimSpace(act.AfterColumn); after != "" && !strings.EqualFold(after, col.Name) {
				moving := cols[foundIdx]
				cols = append(cols[:foundIdx], cols[foundIdx+1:]...)
				reInserted := false
				for idx, existing := range cols {
					if strings.EqualFold(existing.def.Name, after) {
						cols = append(cols[:idx+1], append([]rebuildCol{moving}, cols[idx+1:]...)...)
						reInserted = true
						break
					}
				}
				if !reInserted {
					cols = append(cols, moving)
				}
			}

		case "RENAME_COLUMN":
			oldName := strings.TrimSpace(act.OldName)
			newName := strings.TrimSpace(act.NewName)
			if newName == "" {
				newName = strings.TrimSpace(act.Column.Name)
			}
			if oldName == "" || newName == "" {
				return nil, errors.New("both old and new column names are required when renaming a column")
			}
			found := false
			for i := range cols {
				if strings.EqualFold(cols[i].def.Name, oldName) {
					cols[i].def.Name = newName
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("column to rename not found: %s", oldName)
			}
			renameRefs(oldName, newName)

		case "DROP_COLUMN":
			dropName := strings.TrimSpace(act.Column.Name)
			if dropName == "" {
				dropName = strings.TrimSpace(act.OldName)
			}
			nextCols := make([]rebuildCol, 0, len(cols))
			removed := false
			for _, c := range cols {
				if strings.EqualFold(c.def.Name, dropName) {
					removed = true
					continue
				}
				nextCols = append(nextCols, c)
			}
			if !removed {
				return nil, fmt.Errorf("column to drop not found: %s", dropName)
			}
			cols = nextCols

			// Prune dropped column from indexes
			nextIdx := make([]driver.IndexDef, 0, len(indexes))
			for _, idx := range indexes {
				remCols := make([]string, 0, len(idx.Columns))
				for _, ic := range idx.Columns {
					if !strings.EqualFold(ic, dropName) {
						remCols = append(remCols, ic)
					}
				}
				if len(remCols) > 0 {
					idx.Columns = remCols
					nextIdx = append(nextIdx, idx)
				}
			}
			indexes = nextIdx

			// Prune dropped column from foreign keys
			nextFKs := make([]driver.ForeignKeyDef, 0, len(fks))
			for _, fk := range fks {
				usesDropped := false
				for _, fc := range fk.Columns {
					if strings.EqualFold(fc, dropName) {
						usesDropped = true
						break
					}
				}
				if !usesDropped {
					nextFKs = append(nextFKs, fk)
				}
			}
			fks = nextFKs

		case "ADD_INDEX":
			idx := act.Index
			if len(idx.Columns) == 0 {
				return nil, errors.New("index columns cannot be empty")
			}
			if strings.TrimSpace(idx.Name) == "" {
				idx.Name = "idx_" + finalTable + "_" + strings.Join(idx.Columns, "_")
			}
			indexes = append(indexes, idx)

		case "DROP_INDEX":
			targetIdx := strings.TrimSpace(act.Index.Name)
			filtered := make([]driver.IndexDef, 0, len(indexes))
			for _, idx := range indexes {
				if !strings.EqualFold(idx.Name, targetIdx) {
					filtered = append(filtered, idx)
				}
			}
			indexes = filtered

		case "ADD_FOREIGN_KEY":
			fk := act.ForeignKey
			if len(fk.Columns) == 0 || strings.TrimSpace(fk.RefTable) == "" || len(fk.RefColumns) == 0 {
				return nil, errors.New("foreign key columns and referenced target cannot be empty")
			}
			if strings.TrimSpace(fk.Name) == "" {
				fk.Name = fmt.Sprintf("fk_%s_%s", finalTable, strings.Join(fk.Columns, "_"))
			}
			fks = append(fks, fk)

		case "DROP_FOREIGN_KEY":
			targetFK := strings.TrimSpace(act.ForeignKey.Name)
			if targetFK == "" {
				targetFK = strings.TrimSpace(act.OldName)
			}
			filtered := make([]driver.ForeignKeyDef, 0, len(fks))
			for _, fk := range fks {
				if !strings.EqualFold(fk.Name, targetFK) {
					filtered = append(filtered, fk)
				}
			}
			fks = filtered

		case "RENAME_TABLE":
			if strings.TrimSpace(act.NewName) == "" {
				return nil, errors.New("new table name cannot be empty")
			}
			finalTable = strings.TrimSpace(act.NewName)
		}
	}

	if len(cols) == 0 {
		return nil, errors.New("cannot drop all columns from a table")
	}

	tmpTable := "__dbharbor_tmp_" + req.Table
	if !req.PreviewOnly {
		tmpTable = fmt.Sprintf("__dbharbor_tmp_%s_%d", req.Table, time.Now().UnixNano()%100000)
	}

	colDefs := make([]driver.ColumnDef, len(cols))
	destCols := make([]string, len(cols))
	srcExprs := make([]string, len(cols))
	for i, rc := range cols {
		colDefs[i] = rc.def
		destCols[i] = sqlbase.QuoteIdent(sqlbase.DialectSQLite, rc.def.Name)
		srcExprs[i] = rc.srcExpr
	}

	createTmpDDL, err := sqlbase.BuildCreateTableDDL(sqlbase.DialectSQLite, driver.CreateTableRequest{
		Table:       tmpTable,
		Columns:     colDefs,
		ForeignKeys: fks,
	})
	if err != nil {
		return nil, err
	}

	qOrig := sqlbase.QuoteIdent(sqlbase.DialectSQLite, req.Table)
	qTmp := sqlbase.QuoteIdent(sqlbase.DialectSQLite, tmpTable)
	qFinal := sqlbase.QuoteIdent(sqlbase.DialectSQLite, finalTable)

	copySQL := fmt.Sprintf("INSERT INTO %s (%s)\nSELECT %s FROM %s",
		qTmp, strings.Join(destCols, ", "), strings.Join(srcExprs, ", "), qOrig)
	dropOrigSQL := "DROP TABLE " + qOrig
	renameTmpSQL := fmt.Sprintf("ALTER TABLE %s RENAME TO %s", qTmp, qFinal)

	var indexSQLs []string
	for _, idx := range indexes {
		if len(idx.Columns) == 0 {
			continue
		}
		qIdxCols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			qIdxCols[i] = sqlbase.QuoteIdent(sqlbase.DialectSQLite, c)
		}
		uniq := ""
		if idx.Unique {
			uniq = "UNIQUE "
		}
		idxName := idx.Name
		if idxName == "" {
			idxName = "idx_" + finalTable + "_" + strings.Join(idx.Columns, "_")
		}
		indexSQLs = append(indexSQLs, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
			uniq, sqlbase.QuoteIdent(sqlbase.DialectSQLite, idxName), qFinal, strings.Join(qIdxCols, ", ")))
	}

	// Preserve existing triggers on the original table (when table name unchanged)
	var triggerSQLs []string
	if strings.EqualFold(finalTable, req.Table) {
		tRows, tErr := d.db.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND sql IS NOT NULL`, req.Table)
		if tErr == nil {
			for tRows.Next() {
				var tSQL string
				if tRows.Scan(&tSQL) == nil && strings.TrimSpace(tSQL) != "" {
					triggerSQLs = append(triggerSQLs, tSQL)
				}
			}
			_ = tRows.Close()
		}
	}

	txSteps := make([]string, 0, 4+len(indexSQLs)+len(triggerSQLs))
	txSteps = append(txSteps, createTmpDDL, copySQL, dropOrigSQL, renameTmpSQL)
	txSteps = append(txSteps, indexSQLs...)
	txSteps = append(txSteps, triggerSQLs...)

	previewList := make([]string, 0, len(txSteps)+4)
	previewList = append(previewList, "PRAGMA foreign_keys = OFF", "BEGIN TRANSACTION")
	previewList = append(previewList, txSteps...)
	previewList = append(previewList, "COMMIT", "PRAGMA foreign_keys = ON")

	if req.PreviewOnly {
		return previewList, nil
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return previewList, err
	}
	defer conn.Close()

	_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return previewList, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, step := range txSteps {
		if _, err := tx.ExecContext(ctx, step); err != nil {
			return previewList, fmt.Errorf("SQLite table rebuild failed [%s]: %w", step, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return previewList, err
	}
	return previewList, nil
}
