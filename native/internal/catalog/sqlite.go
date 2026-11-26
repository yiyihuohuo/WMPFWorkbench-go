package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

// Keep the old inspection database usable by existing scripts. A rebuild replaces
// rows in one transaction so removed apps cannot survive as phantom cache hits.
func writeSQLite(ctx context.Context, path string, idx Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS apps(appid TEXT PRIMARY KEY, name TEXT, name_source TEXT,sources TEXT, versions TEXT, subpackage_files TEXT,package_count INTEGER, pkg_bytes INTEGER, pkg_encrypted_all INTEGER,local_files INTEGER, local_bytes INTEGER, local_groups TEXT, icon_glob TEXT);
 CREATE TABLE IF NOT EXISTS base_libs(source TEXT,version TEXT,real_name TEXT,bytes INTEGER,plaintext INTEGER,PRIMARY KEY(source,version,real_name));DELETE FROM apps;DELETE FROM base_libs;`)
	if err != nil {
		return err
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	for _, a := range idx.Apps {
		_, err = tx.ExecContext(ctx, `INSERT INTO apps VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.AppID, a.Name, a.NameSource, js(a.Sources), js(a.Versions), js(a.SubpackageFiles), a.PackageCount, a.PkgBytes, a.PkgEncryptedAll, a.LocalFiles, a.LocalBytes, js(a.LocalGroups), a.IconGlob)
		if err != nil {
			return err
		}
	}
	for source, libs := range idx.BaseLibs {
		for _, l := range libs {
			_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO base_libs VALUES(?,?,?,?,?)`, source, l.Version, l.RealName, l.Bytes, l.Plaintext)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
