package framework

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	homedir "github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
)

func ExecCmd(cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	log.Debugf("Running %#v", cmd.Args)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to exec %#v: %v", cmd.Args, err)
	}
	return nil
}

// GoCmdOptions is the base option set for "go" subcommands.
type GoCmdOptions struct {
	// BinName is the name of the compiled binary, passed to -o.
	BinName string
	// Args are args passed to "go {cmd}", aside from "-o {bin_name}" and
	// test binary args.
	// These apply to build, clean, get, install, list, run, and test.
	Args []string
	// PackagePath is the path to the main (go build) or test (go test) packages.
	PackagePath string
	// Env is a list of environment variables to pass to the cmd;
	// exec.Command.Env is set to this value.
	Env []string
	// Dir is the dir to run "go {cmd}" in; exec.Command.Dir is set to this value.
	Dir string
}

// GoTestOptions is the set of options for "go test".
type GoTestOptions struct {
	GoCmdOptions
	// TestBinaryArgs are args passed to the binary compiled by "go test".
	TestBinaryArgs []string
}

var validVendorCmds = map[string]struct{}{
	"build":   struct{}{},
	"clean":   struct{}{},
	"get":     struct{}{},
	"install": struct{}{},
	"list":    struct{}{},
	"run":     struct{}{},
	"test":    struct{}{},
}

// GoBuild runs "go build" configured with opts.
func GoBuild(opts GoCmdOptions) error {
	return GoCmd("build", opts)
}

// GoTest runs "go test" configured with opts.
func GoTest(opts GoTestOptions) error {
	bargs, err := opts.getGeneralArgsWithCmd("test")
	if err != nil {
		return err
	}
	bargs = append(bargs, opts.TestBinaryArgs...)
	c := exec.Command("go", bargs...)
	opts.setCmdFields(c)
	return ExecCmd(c)
}

// GoCmd runs "go {cmd}".
func GoCmd(cmd string, opts GoCmdOptions) error {
	bargs, err := opts.getGeneralArgsWithCmd(cmd)
	if err != nil {
		return err
	}
	c := exec.Command("go", bargs...)
	opts.setCmdFields(c)
	return ExecCmd(c)
}

func (opts GoCmdOptions) getGeneralArgsWithCmd(cmd string) ([]string, error) {
	// Go subcommands with more than one child command must be passed as
	// multiple arguments instead of a spaced string, ex. "go mod init".
	bargs := []string{}
	for _, c := range strings.Split(cmd, " ") {
		if ct := strings.TrimSpace(c); ct != "" {
			bargs = append(bargs, ct)
		}
	}
	if len(bargs) == 0 {
		return nil, fmt.Errorf("the go binary cannot be run without subcommands")
	}

	if opts.BinName != "" {
		bargs = append(bargs, "-o", opts.BinName)
	}
	bargs = append(bargs, opts.Args...)

	if goModOn, err := GoModOn(); err != nil {
		return nil, err
	} else if goModOn {
		// Does vendor exist?
		info, err := os.Stat("vendor")
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		// Does the first "go" subcommand accept -mod=vendor?
		_, ok := validVendorCmds[bargs[0]]
		// TODO: remove needsModVendor when
		// https://github.com/golang/go/issues/32471 is resolved.
		if err == nil && info.IsDir() && ok && needsModVendor() {
			bargs = append(bargs, "-mod=vendor")
		}
	}

	if opts.PackagePath != "" {
		bargs = append(bargs, opts.PackagePath)
	}
	return bargs, nil
}

// needsModVendor resolves https://github.com/golang/go/issues/32471,
// where any flags in GOFLAGS that are also set in the CLI are
// duplicated, causing 'go' invocation errors.
// TODO: remove once the issue is resolved.
func needsModVendor() bool {
	return !strings.Contains(os.Getenv("GOFLAGS"), "-mod=vendor")
}

func (opts GoCmdOptions) setCmdFields(c *exec.Cmd) {
	c.Env = append(c.Env, os.Environ()...)
	if len(opts.Env) != 0 {
		c.Env = append(c.Env, opts.Env...)
	}
	if opts.Dir != "" {
		c.Dir = opts.Dir
	}
}

// From https://github.com/golang/go/wiki/Modules:
//	You can activate module support in one of two ways:
//	- Invoke the go command in a directory with a valid go.mod file in the
//      current directory or any parent of it and the environment variable
//      GO111MODULE unset (or explicitly set to auto).
//	- Invoke the go command with GO111MODULE=on environment variable set.
//
// GoModOn returns true if Go modules are on in one of the above two ways.
func GoModOn() (bool, error) {
	v, ok := os.LookupEnv(GoModEnv)
	if !ok {
		return true, nil
	}
	switch v {
	case "", "auto", "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("unknown environment setting GO111MODULE=%s", v)
	}
}

func WdInGoPathSrc() (bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	goPath, ok := os.LookupEnv(GoPathEnv)
	if !ok || goPath == "" {
		hd, err := getHomeDir()
		if err != nil {
			return false, err
		}
		goPath = filepath.Join(hd, "go")
	}
	return strings.HasPrefix(wd, filepath.Join(goPath, "src")), nil
}

const (
	GoPathEnv  = "GOPATH"
	GoFlagsEnv = "GOFLAGS"
	GoModEnv   = "GO111MODULE"
	SrcDir     = "src"

	fsep              = string(filepath.Separator)
	mainFile          = "main.go"
	managerMainFile   = "cmd" + fsep + "manager" + fsep + mainFile
	buildDockerfile   = "build" + fsep + "Dockerfile"
	rolesDir          = "roles"
	requirementsFile  = "requirements.yml"
	moleculeDir       = "molecule"
	helmChartsDir     = "helm-charts"
	goModFile         = "go.mod"
	defaultPermission = 0644

	noticeColor = "\033[1;36m%s\033[0m"
)

// OperatorType - the type of operator
type OperatorType = string

const (
	// OperatorTypeGo - golang type of operator.
	OperatorTypeGo OperatorType = "go"
	// OperatorTypeAnsible - ansible type of operator.
	OperatorTypeAnsible OperatorType = "ansible"
	// OperatorTypeHelm - helm type of operator.
	OperatorTypeHelm OperatorType = "helm"
	// OperatorTypeUnknown - unknown type of operator.
	OperatorTypeUnknown OperatorType = "unknown"
)

type ErrUnknownOperatorType struct {
	Type string
}

func (e ErrUnknownOperatorType) Error() string {
	if e.Type == "" {
		return "unknown operator type"
	}
	return fmt.Sprintf(`unknown operator type "%v"`, e.Type)
}

// MustInProjectRoot checks if the current dir is the project root, and exits
// if not.
func MustInProjectRoot() {
	if err := CheckProjectRoot(); err != nil {
		log.Fatal(err)
	}
}

const configFile = "PROJECT"

// HasProjectFile returns true if the project is configured as a kubebuilder
// project.
func HasProjectFile() bool {
	_, err := os.Stat(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		log.Fatalf("Failed to read PROJECT file to detect kubebuilder project: %v", err)
	}
	return true
}

// CheckProjectRoot checks if the current dir is the project root, and returns
// an error if not.
// "build/Dockerfile" may not be present in all projects
// todo: scaffold Project file for Ansible and Helm with the type information
func CheckProjectRoot() error {
	if HasProjectFile() {
		return nil
	}

	// todo(camilamacedo86): remove the following check when we no longer support the legacy scaffold layout
	// If the current directory has a "build/Dockerfile", then it is safe to say
	// we are at the project root.
	if _, err := os.Stat(buildDockerfile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("must run command in project root dir: project structure requires %s",
				buildDockerfile)
		}
		return fmt.Errorf("error while checking if current directory is the project root: %v", err)
	}
	return nil
}

func CheckGoProjectCmd(cmd *cobra.Command) error {
	if IsOperatorGo() {
		return nil
	}
	return fmt.Errorf("'%s' can only be run for Go operators; %s or %s do not exist",
		cmd.CommandPath(), managerMainFile, mainFile)
}

func MustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: (%v)", err)
	}
	return wd
}

func getHomeDir() (string, error) {
	hd, err := homedir.Dir()
	if err != nil {
		return "", err
	}
	return homedir.Expand(hd)
}

// TODO(hasbro17): If this function is called in the subdir of
// a module project it will fail to parse go.mod and return
// the correct import path.
// This needs to be fixed to return the pkg import path for any subdir
// in order for `generate csv` to correctly form pkg imports
// for API pkg paths that are not relative to the root dir.
// This might not be fixable since there is no good way to
// get the project root from inside the subdir of a module project.
//
// GetGoPkg returns the current directory's import path by parsing it from
// wd if this project's repository path is rooted under $GOPATH/src, or
// from go.mod the project uses Go modules to manage dependencies.
// If the project has a go.mod then wd must be the project root.
//
// Example: "github.com/example-inc/app-operator"
func GetGoPkg() string {
	// Default to reading from go.mod, as it should usually have the (correct)
	// package path, and no further processing need be done on it if so.
	if _, err := os.Stat(goModFile); err != nil && !os.IsNotExist(err) {
		log.Fatalf("Failed to read go.mod: %v", err)
	} else if err == nil {
		b, err := ioutil.ReadFile(goModFile)
		if err != nil {
			log.Fatalf("Read go.mod: %v", err)
		}
		mf, err := modfile.Parse(goModFile, b, nil)
		if err != nil {
			log.Fatalf("Parse go.mod: %v", err)
		}
		if mf.Module != nil && mf.Module.Mod.Path != "" {
			return mf.Module.Mod.Path
		}
	}

	// Then try parsing package path from $GOPATH (set env or default).
	goPath, ok := os.LookupEnv(GoPathEnv)
	if !ok || goPath == "" {
		hd, err := getHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		goPath = filepath.Join(hd, "go", "src")
	} else {
		// MustSetWdGopath is necessary here because the user has set GOPATH,
		// which could be a path list.
		goPath = MustSetWdGopath(goPath)
	}
	if !strings.HasPrefix(MustGetwd(), goPath) {
		log.Fatal("Could not determine project repository path: $GOPATH not set, wd in default $HOME/go/src," +
			" or wd does not contain a go.mod")
	}
	return parseGoPkg(goPath)
}

func parseGoPkg(gopath string) string {
	goSrc := filepath.Join(gopath, SrcDir)
	wd := MustGetwd()
	pathedPkg := strings.Replace(wd, goSrc, "", 1)
	// Make sure package only contains the "/" separator and no others, and
	// trim any leading/trailing "/".
	return strings.Trim(filepath.ToSlash(pathedPkg), "/")
}

// GetOperatorType returns type of operator is in cwd.
// This function should be called after verifying the user is in project root.
func GetOperatorType() OperatorType {
	switch {
	// TODO: Differentiate between legacy and KB Go projects
	case IsOperatorGo():
		return OperatorTypeGo
	case IsOperatorAnsible():
		return OperatorTypeAnsible
	case IsOperatorHelm():
		return OperatorTypeHelm
	}
	return OperatorTypeUnknown
}

func IsOperatorGo() bool {
	// todo: in the future we should check the plugin prefix to ensure the operator type
	// for now, we can assume that any project with the kubebuilder layout is Go Type
	if HasProjectFile() {
		return true
	}

	// todo: remove the following code when the legacy layout is no longer supported
	// we can check it using the Project File
	_, err := os.Stat(managerMainFile)
	if err == nil || os.IsExist(err) {
		return true
	}
	// Aware of an alternative location for main.go.
	_, err = os.Stat(mainFile)
	return err == nil || os.IsExist(err)
}

func IsOperatorAnsible() bool {
	stat, err := os.Stat(rolesDir)
	if (err == nil && stat.IsDir()) || os.IsExist(err) {
		return true
	}
	stat, err = os.Stat(moleculeDir)
	if (err == nil && stat.IsDir()) || os.IsExist(err) {
		return true
	}
	_, err = os.Stat(requirementsFile)
	return err == nil || os.IsExist(err)
}

func IsOperatorHelm() bool {
	stat, err := os.Stat(helmChartsDir)
	return (err == nil && stat.IsDir()) || os.IsExist(err)
}

// MustGetGopath gets GOPATH and ensures it is set and non-empty. If GOPATH
// is not set or empty, MustGetGopath exits.
func MustGetGopath() string {
	gopath, ok := os.LookupEnv(GoPathEnv)
	if !ok || len(gopath) == 0 {
		log.Fatal("GOPATH env not set")
	}
	return gopath
}

// MustSetWdGopath sets GOPATH to the first element of the path list in
// currentGopath that prefixes the wd, then returns the set path.
// If GOPATH cannot be set, MustSetWdGopath exits.
func MustSetWdGopath(currentGopath string) string {
	var (
		newGopath   string
		cwdInGopath bool
		wd          = MustGetwd()
	)
	for _, newGopath = range filepath.SplitList(currentGopath) {
		if strings.HasPrefix(filepath.Dir(wd), newGopath) {
			cwdInGopath = true
			break
		}
	}
	if !cwdInGopath {
		log.Fatalf("Project not in $GOPATH")
	}
	if err := os.Setenv(GoPathEnv, newGopath); err != nil {
		log.Fatal(err)
	}
	return newGopath
}

var flagRe = regexp.MustCompile("(.* )?-v(.* )?")

// SetGoVerbose sets GOFLAGS="${GOFLAGS} -v" if GOFLAGS does not
// already contain "-v" to make "go" command output verbose.
func SetGoVerbose() error {
	gf, ok := os.LookupEnv(GoFlagsEnv)
	if !ok || len(gf) == 0 {
		return os.Setenv(GoFlagsEnv, "-v")
	}
	if !flagRe.MatchString(gf) {
		return os.Setenv(GoFlagsEnv, gf+" -v")
	}
	return nil
}

// CheckRepo ensures dependency manager type and repo are being used in combination
// correctly, as different dependency managers have different Go environment
// requirements.
func CheckRepo(repo string) error {
	inGopathSrc, err := WdInGoPathSrc()
	if err != nil {
		return err
	}
	if !inGopathSrc && repo == "" {
		return fmt.Errorf(`flag --repo must be set if the working directory is not in $GOPATH/src.
		See "operator-sdk new -h"`)
	}
	return nil
}

// CheckGoModules ensures that go modules are enabled.
func CheckGoModules() error {
	goModOn, err := GoModOn()
	if err != nil {
		return err
	}
	if !goModOn {
		return fmt.Errorf(`using go modules requires GO111MODULE="on", "auto", or unset.` +
			` More info: https://v0-19-x.sdk.operatorframework.io/docs/golang/quickstart/#a-note-on-dependency-management`)
	}
	return nil
}

// PrintDeprecationWarning prints a colored warning wrapping msg to the terminal.
func PrintDeprecationWarning(msg string) {
	fmt.Fprintf(os.Stderr, noticeColor, "[Deprecation Notice] "+msg+"\n")
}

// RewriteFileContents adds the provided content before the last occurrence of the word label
// and rewrites the file with the new content.
func RewriteFileContents(filename, instruction, content string) error {
	text, err := ioutil.ReadFile(filename)

	if err != nil {
		return fmt.Errorf("error in getting contents from the file, %v", err)
	}

	existingContent := string(text)

	modifiedContent, err := appendContent(existingContent, instruction, content)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(filename, []byte(modifiedContent), defaultPermission)
	if err != nil {
		return fmt.Errorf("error writing modified contents to file, %v", err)
	}
	return nil
}

func appendContent(fileContents, instruction, content string) (string, error) {
	labelIndex := strings.LastIndex(fileContents, instruction)

	if labelIndex == -1 {
		return "", fmt.Errorf("instruction not present previously in dockerfile")
	}

	separationIndex := strings.Index(fileContents[labelIndex:], "\n")
	if separationIndex == -1 {
		return "", fmt.Errorf("no new line at the end of dockerfile command %s", fileContents[labelIndex:])
	}

	index := labelIndex + separationIndex + 1

	newContent := fileContents[:index] + content + fileContents[index:]

	return newContent, nil

}
