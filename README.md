# sshcontainers - A unique container per SSH client public key

## Background

I had the idea after reading [this interesting tweet][tweet]: an SSH server that
would present a different Docker container for each SSH public key that clients
use to connect to it. So that's what this is.

## Dangerous tl;dr

```
ssh-keygen -f hostkey -P ""
docker run \
  -it \
  -p 2222:2222 \
  -v $(pwd)/hostkey:/hostkey \
  -v /var/run/docker.sock:/var/run/docker.sock \
  ghcr.io/aidansteele/sshcontainers \
  -h /hostkey \
  --wildcard

# now in another tab run this:
ssh -p 2222 127.0.0.1
```

## Usage

```
Usage of sshcontainers:
  -a, --address string                ip and port to listen on (default "0.0.0.0:2222")
  -u, --authorized-keys-path string   path to authorized ssh users
  -h, --host-key-path string          path to ssh host key
  -i, --image string                  image to use (must already be pulled) (default "ubuntu")
  -s, --shell string                  shell to use when command not provided by client (default "/bin/bash")
      --wildcard                      allow ANY ssh public key to connect instead of allow list in --authorized-key-paths. be careful
```

You can generate a host key by using `ssh-keygen` and passing the path to the 
private key (not the file ending in `.pub`).

The authorized keys file follows the format of `~/.ssh/authorized_keys`. It uses
the comment field as a way to lookup container names, i.e. a file that looks like
this:

```
ssh-rsa AAAAB3Nz...<truncated>Rn6DfqmebE= somecontainer
```

will cause logins from that SSH user public key to create a container named
`somecontainer` and connect sessions to it.

Alternatively, instead of an authorized keys file you can pass `--wildcard` and
**any** SSH public key will be accepted. Container names will be of the form
`wildcard${key fingerprint}`.

[tweet]: https://twitter.com/supersat/status/1320145718741323776
