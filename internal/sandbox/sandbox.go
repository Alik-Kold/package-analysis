package sandbox

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ossf/package-analysis/internal/log"
)

const (
	podmanBin     = "podman"
	runtimeBin    = "/usr/local/bin/runsc_compat.sh"
	rootDir       = "/var/run/runsc"
	runLogFile    = "runsc.log.boot"
	logDirPattern = "sandbox_logs_"

	// networkName is the name of the podman network defined in
	// tools/network/podman-analysis.conflist. This network is the network
	// used by the sandbox during analysis to separate the sandbox traffic
	// from the host.
	networkName = "analysis-net"
)

type RunStatus uint8

const (
	// RunStatusUnknown is used when some other issue occurred that prevented
	// an attempt to run the command.
	RunStatusUnknown = iota

	// RunStatusSuccess is used to indicate that the command being executed
	// successfully.
	RunStatusSuccess

	// RunStatusFailure is used to indicate that the command exited with some
	// failure.
	RunStatusFailure

	// RunStatusTimeout is used to indicate that the command failed to complete
	// within the allowed timeout.
	RunStatusTimeout
)

type RunResult struct {
	logPath string
	status  RunStatus
	stderr  *bytes.Buffer
	stdout  *bytes.Buffer
}

// Log returns the log file recorded during a run.
//
// This log will contain strace data.
func (r *RunResult) Log() (io.ReadCloser, error) {
	return os.Open(r.logPath)
}

func (r *RunResult) Status() RunStatus {
	if r != nil {
		return r.status
	}
	return RunStatusUnknown
}

func (r *RunResult) Stdout() []byte {
	return r.stdout.Bytes()
}

func (r *RunResult) Stderr() []byte {
	return r.stderr.Bytes()
}

type Sandbox interface {
	// Run will execute the supplied command and args in the sandbox.
	//
	// The container used to execute the command is reused until Clean() is
	// called.
	//
	// If there is an error while using the sandbox an error will be returned.
	//
	// The result of the supplied command will be returned in an instance of
	// RunResult.
	Run(...string) (*RunResult, error)
	UploadFileToContainer(srcFile string, destFile string) *exec.Cmd
	// Clean cleans up a Sandbox.
	//
	// Once called the Sandbox cannot be used again.
	Clean() error
}

// volume represents a volume mapping between a host src and a container dest.
type volume struct {
	src  string
	dest string
}

func (v volume) args() []string {
	return []string{
		"-v",
		fmt.Sprintf("%s:%s", v.src, v.dest),
	}
}

// Implements the Sandbox interface using "podman".
type podmanSandbox struct {
	image      string
	tag        string
	id         string
	container  string
	noPull     bool
	rawSockets bool
	strace     bool
	offline    bool
	logPackets bool
	logStdOut  bool
	logStdErr  bool
	echoStdOut bool
	echoStdErr bool
	volumes    []volume
}

func (s *podmanSandbox) UploadFileToContainer(srcFile string, destFile string) *exec.Cmd {
	destParam := fmt.Sprintf("%s:%s", s.container, destFile)
	args := []string{
		"cp",
		srcFile,
		destParam,
	}
	return podman(args...)
}

type (
	Option interface{ set(*podmanSandbox) }
	option func(*podmanSandbox) // option implements Option.
)

func (o option) set(sb *podmanSandbox) { o(sb) }

func New(image string, options ...Option) Sandbox {
	sb := &podmanSandbox{
		image:      image,
		tag:        "",
		container:  "",
		noPull:     false,
		rawSockets: false,
		strace:     false,
		offline:    false,
		logPackets: false,
		logStdOut:  false,
		logStdErr:  false,
		echoStdOut: false,
		echoStdErr: false,
		volumes:    make([]volume, 0),
	}
	for _, o := range options {
		o.set(sb)
	}
	return sb
}

// EnableRawSockets allows use of raw sockets in the sandbox.
func EnableRawSockets() Option {
	return option(func(sb *podmanSandbox) { sb.rawSockets = true })
}

// EnableStrace enables strace functionality for the sandbox.
func EnableStrace() Option {
	return option(func(sb *podmanSandbox) { sb.strace = true })
}

// Offline disables network functionality for the sandbox.
func Offline() Option {
	return option(func(sb *podmanSandbox) { sb.offline = true })
}

// EnablePacketLogging enables packet logging for the sandbox.
func EnablePacketLogging() Option {
	return option(func(sb *podmanSandbox) { sb.logPackets = true })
}

// LogStdOut enables wrapping each line of stdout from sandboxed process
// as a log.Info line in the main container.
func LogStdOut() Option {
	return option(func(sb *podmanSandbox) { sb.logStdOut = true })
}

// LogStdErr enables wrapping each line of stderr from the sandboxed process
// as log.Warn line in the main container.
func LogStdErr() Option {
	return option(func(sb *podmanSandbox) { sb.logStdErr = true })
}

// EchoStdOut enables simple echoing of the sandboxed process stdout.
func EchoStdOut() Option {
	return option(func(sb *podmanSandbox) { sb.echoStdOut = true })
}

// EchoStdErr enables simple echoing of the sandboxed process stderr.
func EchoStdErr() Option {
	return option(func(sb *podmanSandbox) { sb.echoStdErr = true })
}

// NoPull will disable the image for the sandbox from being pulled during Init.
func NoPull() Option {
	return option(func(sb *podmanSandbox) { sb.noPull = true })
}

// Volume can be used to specify an additional volume map into the container.
//
// src is the path in the host that will be mapped to the dest path.
func Volume(src, dest string) Option {
	return option(func(sb *podmanSandbox) {
		sb.volumes = append(sb.volumes, volume{
			src:  src,
			dest: dest,
		})
	})
}

func Tag(tag string) Option {
	return option(func(sb *podmanSandbox) { sb.tag = tag })
}

func removeAllLogs() error {
	matches, err := filepath.Glob(path.Join(os.TempDir(), logDirPattern+"*"))
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			return err
		}
	}
	return nil
}

func podman(args ...string) *exec.Cmd {
	args = append([]string{
		"--cgroup-manager=cgroupfs",
		"--events-backend=file",
	}, args...)
	log.Debug("podman", "args", args)
	return exec.Command(podmanBin, args...)
}

func podmanRun(args ...string) error {
	cmd := podman(args...)
	return cmd.Run()
}

func podmanPrune() error {
	return podmanRun("image", "prune", "-f")
}

func podmanCleanContainers() error {
	return podmanRun("rm", "--all", "--force")
}

func (s *podmanSandbox) pullImage() error {
	return podmanRun("pull", s.imageWithTag())
}

func (s *podmanSandbox) createContainer() (string, error) {
	args := []string{
		"create",
		"--runtime=" + runtimeBin,
		"--init",
	}

	networkArgs := []string{
		"--dns=8.8.8.8",  // Manually specify DNS to bypass kube-dns and
		"--dns=8.8.4.4",  // allow for tighter firewall rules that block
		"--dns-search=.", // network traffic to private IP address ranges.
		"--network=" + networkName,
	}

	if s.offline {
		args = append(args, "--network=none")
	} else {
		args = append(args, networkArgs...)
	}

	args = append(args, s.extraArgs()...)
	args = append(args, s.imageWithTag())
	cmd := podman(args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(buf.Bytes())), nil
}

func (s *podmanSandbox) startContainerCmd(logDir string) *exec.Cmd {
	args := []string{
		"start",
		"--runtime=" + runtimeBin,
		"--runtime-flag=root=" + rootDir,
		"--runtime-flag=debug-log=" + path.Join(logDir, "runsc.log.%COMMAND%"),
	}
	if s.rawSockets {
		args = append(args, "--runtime-flag=net-raw")
	}
	if s.strace {
		args = append(args, "--runtime-flag=strace")
	}
	if s.logPackets {
		args = append(args, "--runtime-flag=log-packets")
	}
	args = append(args, s.container)

	return podman(args...)
}

func (s *podmanSandbox) stopContainerCmd() *exec.Cmd {
	return podman("stop", s.container)
}

func (s *podmanSandbox) forceStopContainer() error {
	return podmanRun(
		"stop",
		"-t=5", // Wait a max of 5 seconds for a graceful stop.
		"-i",   // Ignore any errors of the specified container not being in the store.
		s.container)
}

func (s *podmanSandbox) execContainerCmd(execArgs []string) *exec.Cmd {
	args := []string{
		"exec",
		s.container,
	}
	args = append(args, execArgs...)
	return podman(args...)
}

func (s *podmanSandbox) extraArgs() []string {
	args := make([]string, 0)
	for _, v := range s.volumes {
		args = append(args, v.args()...)
	}
	return args
}

func (s *podmanSandbox) imageWithTag() string {
	tag := "latest"
	if s.tag != "" {
		tag = s.tag
	}
	return fmt.Sprintf("%s:%s", s.image, tag)
}

// init initializes the sandbox.
func (s *podmanSandbox) init() error {
	if s.container != "" {
		return nil
	}
	// Delete existing logs (if any).
	if err := removeAllLogs(); err != nil {
		return fmt.Errorf("failed removing all logs: %w", err)
	}
	if !s.noPull {
		if err := s.pullImage(); err != nil {
			return fmt.Errorf("error pulling image: %w", err)
		}
	}
	if err := podmanPrune(); err != nil {
		return fmt.Errorf("error pruning images: %w", err)
	}
	if id, err := s.createContainer(); err == nil {
		s.container = id
	} else {
		return fmt.Errorf("error creating container: %w", err)
	}
	return nil
}

// Run implements the Sandbox interface.
func (s *podmanSandbox) Run(args ...string) (*RunResult, error) {
	if err := s.init(); err != nil {
		return &RunResult{}, err
	}

	// Create a place to stash the logs for this run.
	logDir, err := os.MkdirTemp("", logDirPattern)
	if err != nil {
		return &RunResult{}, fmt.Errorf("failed to create log directory: %w", err)
	}
	// Chmod the log dir so it can be read by non-root users. Make the behaviour
	// mimic Mkdir called with 0o777 before umask is applied by applying the
	// umask manually to the permissions.
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	if err := os.Chmod(logDir, fs.FileMode(0o777 & ^umask)); err != nil {
		return &RunResult{}, fmt.Errorf("failed to chmod log directory: %w", err)
	}

	// Prepare the run result.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	result := &RunResult{
		logPath: path.Join(logDir, runLogFile),
		status:  RunStatusUnknown,
		stdout:  &stdout,
		stderr:  &stderr,
	}

	// Prepare stdout and stderr writers
	logOut := log.Writer(log.InfoLevel,
		"args", args)
	defer logOut.Close()
	logErr := log.Writer(log.WarnLevel,
		"args", args)
	defer logErr.Close()

	outWriters := []io.Writer{&stdout}
	if s.logStdOut {
		outWriters = append(outWriters, logOut)
	}
	if s.echoStdOut {
		outWriters = append(outWriters, os.Stdout)
	}
	outWriter := io.MultiWriter(outWriters...)

	errWriters := []io.Writer{&stderr}
	if s.logStdErr {
		errWriters = append(errWriters, logErr)
	}
	if s.echoStdErr {
		errWriters = append(errWriters, os.Stdout)
	}
	errWriter := io.MultiWriter(errWriters...)

	log.Debug("upload certs to container")
	uploadCmd := s.UploadFileToContainer("/proxy/certs/ca.crt", "/usr/local/share/ca-certificates/ca.crt") //upload certs to container
	uploadCmd.Stdout = logOut
	uploadCmd.Stderr = logErr
	if err := uploadCmd.Run(); err != nil {
		return result, fmt.Errorf("error uploading file to container: %w", err)
	}

	// Start the container
	startCmd := s.startContainerCmd(logDir)
	startCmd.Stdout = logOut
	startCmd.Stderr = logErr
	if err := startCmd.Run(); err != nil {
		return result, fmt.Errorf("error starting container: %w", err)
	}

	// Run the command in the sandbox
	cmd := s.execContainerCmd(args)
	cmd.Stdout = outWriter
	cmd.Stderr = errWriter

	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("error execing command: %w", err)
	}

	err = cmd.Wait()
	if err == nil {
		result.status = RunStatusSuccess
	} else if _, ok := err.(*exec.ExitError); ok {
		result.status = RunStatusFailure
		err = nil
	}

	// Stop the container
	stopCmd := s.stopContainerCmd()
	var stopStderr bytes.Buffer
	stopCmd.Stdout = logOut
	stopCmd.Stderr = io.MultiWriter(&stopStderr, logErr)
	if stopErr := stopCmd.Run(); stopErr != nil {
		if strings.Contains(stopStderr.String(), "gofer is still running") {
			// Ignore the error if stderr contains "gofer is still running"
			log.Debug("ignoring 'stop' error - gofer still running")
		} else if err == nil {
			// Don't overwrite the earlier error
			err = fmt.Errorf("error stopping container: %w", stopErr)
		}
	}

	return result, err
}

// Clean implements the Sandbox interface.
func (s *podmanSandbox) Clean() error {
	if s.container == "" {
		return nil
	}
	if err := s.forceStopContainer(); err != nil {
		return err
	}
	return podmanCleanContainers()
}
