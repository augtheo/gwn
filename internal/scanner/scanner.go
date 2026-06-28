package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func Scan(paths []string, depth int) []Workspace {
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		workspaces []Workspace
	)

	for _, root := range paths {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			found := scanDir(root, depth)
			mu.Lock()
			workspaces = append(workspaces, found...)
			mu.Unlock()
		}(root)
	}

	wg.Wait()
	return workspaces
}

func scanDir(root string, depth int) []Workspace {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		workspaces []Workspace
	)

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())

		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			ws := detectWorkspace(p)
			mu.Lock()
			workspaces = append(workspaces, ws)
			mu.Unlock()
		}(path)
	}

	wg.Wait()
	return workspaces
}
