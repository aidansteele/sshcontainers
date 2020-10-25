package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/gliderlabs/ssh"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	gossh "golang.org/x/crypto/ssh"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
)

func main() {
	image := ""
	pflag.StringVarP(&image, "image", "i", "ubuntu", "image to use (must already be pulled)")

	shell := ""
	pflag.StringVarP(&shell, "shell", "s", "/bin/bash", "shell to use when command not provided by client")

	address := ""
	pflag.StringVarP(&address, "address", "a", "0.0.0.0:2222", "ip and port to listen on")

	hostkeyPath := ""
	pflag.StringVarP(&hostkeyPath, "host-key-path", "h", "", "path to ssh host key")

	authorizedKeysPath := ""
	pflag.StringVarP(&authorizedKeysPath, "authorized-keys-path", "u", "", "path to authorized ssh users")

	wildcard := false
	pflag.BoolVar(&wildcard, "wildcard", false, "allow ANY ssh public key to connect instead of allow list in --authorized-key-paths. be careful")

	pflag.Parse()
	if hostkeyPath == "" {
		pflag.Usage()
		return
	}

	keys := map[string]ssh.PublicKey{}

	if !wildcard {
		f, err := os.Open(authorizedKeysPath)
		if err != nil {
			panic(err)
		}

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			key, comment, _, _, err := ssh.ParseAuthorizedKey(scanner.Bytes())
			if err != nil {
				panic(err)
			}

			keys[comment] = key
		}
	}

	docker, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}

	s := &server{
		docker:   docker,
		image:    image,
		shell:    shell,
		keys:     keys,
		wildcard: wildcard,
	}

	sshsrv := &ssh.Server{
		Addr:    address,
		Handler: s.handle,
	}
	
	sshsrv.SetOption(ssh.HostKeyFile(hostkeyPath))
	sshsrv.SetOption(ssh.PublicKeyAuth(s.authenticate))

	err = ssh.ListenAndServe(
		address,
		s.handle,
		ssh.HostKeyFile(hostkeyPath),
		ssh.PublicKeyAuth(s.authenticate),
	)
	if err != nil {
		panic(err)
	}
}

type server struct {
	docker   *client.Client
	image    string
	shell    string
	keys     map[string]ssh.PublicKey
	wildcard bool
}

func (s *server) authenticate(ctx ssh.Context, key ssh.PublicKey) bool {
	if s.wildcard {
		return true
	}

	for _, pubkey := range s.keys {
		if ssh.KeysEqual(key, pubkey) {
			return true
		}
	}

	fmt.Printf("public key %s rejected from client %s\n", gossh.FingerprintLegacyMD5(key), ctx.RemoteAddr())
	return false
}

func (s *server) handle(sess ssh.Session) {
	ctx := sess.Context()

	containerName := ""
	for comment, key := range s.keys {
		if ssh.KeysEqual(key, sess.PublicKey()) {
			containerName = comment
		}
	}

	if s.wildcard {
		fingerprint := gossh.FingerprintLegacyMD5(sess.PublicKey())
		containerName = "wildcard" + strings.ReplaceAll(fingerprint, ":", "")
	}

	fmt.Printf("client %s connected to container %s\n", sess.RemoteAddr(), containerName)
	defer fmt.Printf("client %s disconnected from container %s\n", sess.RemoteAddr(), containerName)

	err := s.ensureContainerStarted(ctx, s.image, containerName)
	if err != nil {
		fmt.Printf("%+v\n", err)
	}

	status, err := s.containerExec(ctx, sess, containerName)
	if err != nil && errors.Cause(err) != context.Canceled {
		fmt.Printf("%+v\n", err)
	}

	sess.Exit(status)
}

func (s *server) ensureContainerStarted(ctx context.Context, image, cid string) error {
	_, err := s.docker.ContainerInspect(ctx, cid)

	if client.IsErrNotFound(err) {
		_, err = s.docker.ContainerCreate(ctx, &container.Config{
			Cmd:          []string{"/usr/bin/tail", "-f", "/dev/null"},
			Image:        image,
			Tty:          false,
			OpenStdin:    true,
			AttachStderr: true,
			AttachStdin:  true,
			AttachStdout: true,
			StdinOnce:    true,
			Volumes:      make(map[string]struct{}),
		}, nil, nil, cid)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	// doesn't hurt to try start an already-started container
	err = s.docker.ContainerStart(ctx, cid, types.ContainerStartOptions{})
	if err != nil {
		return errors.WithStack(err)
	}

	return nil
}

func (s *server) containerExec(ctx context.Context, sess ssh.Session, cid string) (int, error) {
	_, _, tty := sess.Pty()

	cmd := sess.Command()
	if len(cmd) == 0 {
		cmd = []string{s.shell}
	}

	res, err := s.docker.ContainerExecCreate(ctx, cid, types.ExecConfig{
		Cmd:          cmd,
		Env:          sess.Environ(),
		Tty:          tty,
		AttachStdin:  true,
		AttachStderr: true,
		AttachStdout: true,
	})
	if err != nil {
		return -1, errors.WithStack(err)
	}

	stream, err := s.docker.ContainerExecAttach(ctx, res.ID, types.ExecStartCheck{Tty: true})
	if err != nil {
		return -1, errors.WithStack(err)
	}

	go func() {
		defer stream.CloseWrite()
		io.Copy(stream.Conn, sess)
	}()

	outputErr := make(chan error)

	go func() {
		if tty {
			_, err = io.Copy(sess, stream.Reader)
			outputErr <- errors.WithStack(err)
		} else {
			_, err = stdcopy.StdCopy(sess, sess.Stderr(), stream.Reader)
			outputErr <- errors.WithStack(err)
		}
	}()

	filter := filters.NewArgs()
	filter.Add("container", cid)
	filter.Add("event", "exec_die")
	msgs, errs := s.docker.Events(ctx, types.EventsOptions{Filters: filter})

	err = s.docker.ContainerExecStart(ctx, res.ID, types.ExecStartCheck{Tty: true})
	if err != nil {
		return -1, errors.WithStack(err)
	}

	if tty {
		go s.handleResize(ctx, sess, res.ID)
	}

	status := -1

	select {
	case err := <-errs:
		if err != nil {
			return -1, errors.WithStack(err)
		}
	case msg := <-msgs:
		status, _ = strconv.Atoi(msg.Actor.Attributes["exitCode"])
	}

	err = <-outputErr
	return status, err
}

func (s *server) handleResize(ctx context.Context, sess ssh.Session, execid string) {
	_, winCh, _ := sess.Pty()
	for win := range winCh {
		err := s.docker.ContainerExecResize(ctx, execid, types.ResizeOptions{
			Height: uint(win.Height),
			Width:  uint(win.Width),
		})
		if err != nil {
			log.Println(err)
			break
		}
	}
}
