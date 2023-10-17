// Copyright 2023 Dolthub, Inc.
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

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

var FilePattern = "%s.zip"
var DownloadPath = "https://github.com/dolthub/dolt/archive/"
var ExtractedDirPattern = "dolt-%s"
var FakeNarHash = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

var GeneratedFileWarning = `/* WARNING: This file is generated by main.go in github.com/dolthub/dolt-nix-flake. */`

type TemplateArgs struct {
	Warning      string
	DoltRevision string
	DepsHash     string
}

var RevisionSegment = flag.String("revision", "", "a revision path segment for the dolt github flake url; ex: ?ref=tags/v1.20.0, /c3a827c8a8c197402fa955274d667dfecb80e014")

// This is our workspace where we will create file trees, hash them, etc.
// Be sure to clean it up with `env.Close`
type Environment struct {
	// Paths to required external programs we invoke.
	NixProg   string
	GoProg    string
	UnzipProg string

	BaseDir string

	SourceZipUrl string
	SourceZip    string

	// Extracted source code.
	SourceDir string

	GoModuleDir string
	GoCacheDir  string
	GoPathDir   string

	GoDownloadPath      string
	GoDownloadSumDBPath string
}

func (e *Environment) Close() {
	os.RemoveAll(e.BaseDir)
}

func NewEnvironment(nixprog, revision string) (*Environment, error) {
	goprog, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("did not find required executable, go, in PATH: %w", err)
	}
	unzipprog, err := exec.LookPath("unzip")
	if err != nil {
		return nil, fmt.Errorf("did not find required executable, unzip, in PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "dolt-nix-flake-*")
	if err != nil {
		return nil, fmt.Errorf("could not create temp dir: %w", err)
	}

	filename := fmt.Sprintf(FilePattern, revision)
	extracteddir := fmt.Sprintf(ExtractedDirPattern, revision)

	env := new(Environment)
	env.NixProg = nixprog
	env.GoProg = goprog
	env.UnzipProg = unzipprog
	env.BaseDir = dir
	env.SourceZipUrl = DownloadPath + filename
	env.SourceZip = filepath.Join(env.BaseDir, filename)
	env.SourceDir = filepath.Join(env.BaseDir, extracteddir)
	env.GoModuleDir = filepath.Join(env.SourceDir, "go")
	env.GoCacheDir = filepath.Join(env.BaseDir, "go-cache")
	env.GoPathDir = filepath.Join(env.BaseDir, "go")
	env.GoDownloadPath = filepath.Join(env.GoPathDir, "pkg", "mod", "cache", "download")
	env.GoDownloadSumDBPath = filepath.Join(env.GoDownloadPath, "sumdb")

	err = os.MkdirAll(env.GoCacheDir, 0777)
	if err != nil {
		return nil, fmt.Errorf("could not create temporary GOCACHE directory %v: %w", env.GoCacheDir, err)
	}
	err = os.MkdirAll(env.GoPathDir, 0777)
	if err != nil {
		return nil, fmt.Errorf("could not create temporary GOPATH directory %v: %w", env.GoPathDir, err)
	}

	return env, nil
}

func main() {
	flag.Parse()

	nixprog, err := exec.LookPath("nix")
	if err != nil {
		panic(fmt.Errorf("did not find required executable, nix-hash, in PATH: %v", err))
	}

	err = WriteFlake(*RevisionSegment, FakeNarHash)
	if err != nil {
		panic(err)
	}

	err = NixFlakeUpdate(nixprog)
	if err != nil {
		panic(err)
	}

	// Now flake.lock is updated. Read the dolt rev and calculate our venderHash from it.
	revHash, err := ReadLockContents()
	if err != nil {
		panic(err)
	}

	env, err := NewEnvironment(nixprog, revHash)
	if err != nil {
		panic(err)
	}
	defer env.Close()

	// Download the zip file of the source code.
	err = DownloadFile(env.SourceZip, env.SourceZipUrl)

	// Extract it.
	cmd := exec.Command(env.UnzipProg, env.SourceZip)
	cmd.Dir = env.BaseDir
	err = cmd.Run()
	if err != nil {
		panic(fmt.Errorf("could not run unzip on %s: %v", env.SourceZip, err))
	}

	// Download the dependencies.
	cmd = exec.Command(env.GoProg, "mod", "download")
	cmd.Dir = env.GoModuleDir
	cmd.Env = append(cmd.Env, "GOCACHE="+env.GoCacheDir)
	cmd.Env = append(cmd.Env, "GOPATH="+env.GoPathDir)
	err = cmd.Run()
	if err != nil {
		panic(fmt.Errorf("could not run `go mod download` in %s: %v", cmd.Dir, err))
	}

	// Cleanup sumdb, which does not go in the derivation and should not
	// contribute to the vendorHash.
	err = os.RemoveAll(env.GoDownloadSumDBPath)
	if err != nil {
		panic(fmt.Errorf("could not remote sumdb path at %s: %w", env.GoDownloadSumDBPath, err))
	}

	modhash, err := NixHashDir(env.NixProg, env.GoDownloadPath)
	if err != nil {
		panic(fmt.Errorf("could not nix-hash the download go module dependencies at %s: %w", env.GoDownloadPath, err))
	}

	err = WriteFlake(*RevisionSegment, modhash)
	if err != nil {
		panic(err)
	}
}

// Downloads the given URL to the given destination filename. The directory for
// the given filename must already exist and the file itself must not.
func DownloadFile(dest, url string) error {
	dlf, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("DownloadFile: error creating file %s: %w", dest, err)
	}
	defer dlf.Close()
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("DownloadFile: error GETing %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not fetch %s, got status code: %d", url, resp.StatusCode)
	}
	_, err = io.Copy(dlf, resp.Body)
	if err != nil {
		return fmt.Errorf("could not download entire file: %w", err)
	}
	return nil
}

// Run `nix hash path --base64 --type sha256 $dir` and return the hash for the contents of the directory.
func NixHashDir(prog, dir string) (string, error) {
	cmd := exec.Command(prog, "hash", "path", "--base64", "--type", "sha256", dir)
	cmd.Dir = filepath.Join(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("could not run `nix hash` on %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func NixFlakeUpdate(prog string) error {
	cmd := exec.Command(prog, "flake", "update")
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("could not run `nix flake update`: %w", err)
	}
	return nil
}

func ReadLockContents() (string, error) {
	f, err := os.Open("flake.lock")
	if err != nil {
		return "", fmt.Errorf("could not open file flake.lock: %w", err)
	}
	defer f.Close()
	contents, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("error reading flake.lock: %w", err)
	}

	type Lock struct {
		Rev string `json:"rev"`
	}
	type Dolt struct {
		Lock Lock `json:"locked"`
	}
	type Node struct {
		Dolt Dolt `json:"dolt"`
	}
	type Root struct {
		Node Node `json:"nodes"`
	}
	var unmarshalled Root
	err = json.Unmarshal(contents, &unmarshalled)
	if err != nil {
		return "", fmt.Errorf("error parsing flake.lock: %w", err)
	}
	return unmarshalled.Node.Dolt.Lock.Rev, nil
}

func WriteFlake(revSegment, depsHash string) error {
	tmpl, err := template.ParseFiles("flake.nix.template")
	if err != nil {
		panic(fmt.Errorf("could not load the nix flake template: %w", err))
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not get current working directory: %w", err)
	}
	t, err := os.CreateTemp(wd, "flake.nix.*")
	if err != nil {
		return fmt.Errorf("could not create flake.nix.* temp file in current working directory: %w", err)
	}
	defer os.Remove(t.Name())
	err = tmpl.ExecuteTemplate(t, "flake.nix.template", TemplateArgs{
		Warning:      GeneratedFileWarning,
		DoltRevision: revSegment,
		DepsHash:     depsHash,
	})
	t.Close()
	if err != nil {
		return fmt.Errorf("could not render the nix flake template: %w", err)
	}
	err = os.Rename(t.Name(), "flake.nix")
	if err != nil {
		return fmt.Errorf("could not rename %v to flake.nix: %w", t.Name(), err)
	}
	return nil
}
