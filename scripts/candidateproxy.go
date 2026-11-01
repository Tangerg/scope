//go:build ignore

// Command candidateproxy makes an unpublished snapshot consumable by independent
// Go modules. Its input is the disposable tree created by check-candidate.sh.
package main

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: candidateproxy DISPOSABLE_TREE PROXY_DIR VERSION")
		os.Exit(2)
	}
	if err := stage(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type member struct {
	dir string
	mod *modfile.File
}

func stage(tree, proxy, version string) error {
	workPath := filepath.Join(tree, "go.work")
	raw, err := os.ReadFile(workPath)
	if err != nil {
		return err
	}
	work, err := modfile.ParseWork(workPath, raw, nil)
	if err != nil {
		return err
	}
	members := make([]member, 0, len(work.Use))
	local := make(map[string]bool, len(work.Use))
	for _, u := range work.Use {
		dir := filepath.Join(tree, u.Path)
		path := filepath.Join(dir, "go.mod")
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mod, err := modfile.Parse(path, contents, nil)
		if err != nil {
			return err
		}
		if err := module.Check(mod.Module.Mod.Path, version); err != nil {
			return err
		}
		members = append(members, member{dir, mod})
		local[mod.Module.Mod.Path] = true
	}
	for _, m := range members {
		for _, r := range m.mod.Require {
			if local[r.Mod.Path] {
				if err := m.mod.AddRequire(r.Mod.Path, version); err != nil {
					return err
				}
			}
		}
		data, err := m.mod.Format()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(m.dir, "go.mod"), data, 0644); err != nil {
			return err
		}
	}
	info, err := jsonv2.Marshal(struct {
		Version string
		Time    time.Time
	}{version, time.Unix(0, 0).UTC()})
	if err != nil {
		return err
	}
	for _, m := range members {
		path := m.mod.Module.Mod.Path
		escaped, err := module.EscapePath(path)
		if err != nil {
			return err
		}
		dir := filepath.Join(proxy, filepath.FromSlash(escaped), "@v")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		data, err := m.mod.Format()
		if err != nil {
			return err
		}
		for suffix, body := range map[string][]byte{"list": []byte(version + "\n"), version + ".info": info, version + ".mod": data} {
			if err := os.WriteFile(filepath.Join(dir, suffix), body, 0644); err != nil {
				return err
			}
		}
		output, err := os.Create(filepath.Join(dir, version+".zip"))
		if err != nil {
			return err
		}
		err = modzip.CreateFromDir(output, module.Version{Path: path, Version: version}, m.dir)
		closeErr := output.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if closeErr != nil {
			return closeErr
		}
		if _, err := fmt.Println(strings.TrimPrefix(m.dir, tree+string(os.PathSeparator))); err != nil {
			return err
		}
	}
	return nil
}
