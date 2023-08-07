# Welcome to LocalShow

LocalShow is heavily inspired by [serveo](https://serveo.net) and [ngrok](https://ngrok.com). It was meant as a learning experience for me and an attempt to see just how easy it would be to implement something like `serveo` using examples from the [golang](https://go.dev) standard library.

The inspiration for how to [handle SSH port forwarding](https://github.com/gliderlabs/ssh/blob/cf1ec7e0ccfbfcae02a6be00d1de36125ac7fae4/tcpip.go#L97) came from the wonderful [gliderlabs/ssh](https://github.com/gliderlabs/ssh)

The initial implementation for this was written in one day of coding for around 11 hours (well, 8 if we're to exclude the cuddle breaks with the little one). At the end of the day, `localshow` is able to:

- Accept SSH port forwarding requests
- Use no authentication or use public key authentication for connecting clients
- Create a reverse HTTP(s) tunnels through the reverse tunnel
- Generate a random subdomain or use a user defined subdomain for the tunnel endpoint

This initial implementation is purposely limited to HTTP(s) tunnels. I have no desire to implement TCP tunnels. To be perfectly frank, this project will most likely end up in the graveyard of forgotten personal projects that were born in one day of manic coding and inspiration. If this will turn out to be a maintained project in the end, it will be a pleasant surprise.

If you need something production worthy, I encourage you to use one of the services mentioned above. Those are maintained and developed continuously, while this project will probably only see sparse updates and may be abandoned at any time.

You may still find this project useful as a learning experience or as a starting point for your own project. If you do, I'd love to hear about it!

## What you'll need

To get the best out of localshow, you will need:

* A domain name
* A server with a public IP address
* A wildcard `A` record pointing to the server's IP address
* Optionally, a TLS certificate for the wildcard domain

## How it works

Localshow is a SSH server and a HTTP(S) reverse proxy built into one binary. The SSH server only supports port forwarding. It will allow you to connect and request that the server forward traffic from a port on the server to a port on your local machine. The port you request that the localshow server listen on, should be `80` or `443`. Anything else will return an error. This is just a convention to make it clear that only HTTP traffic will be forwarded from the localshow server, to your local machine.

In reality, the localshow SSH server will completely ignore the port you request and listen on a random port bound to a loopback interface. It will then lie to your client that it started listening on the port you requested. But not to worry, localshow keeps track of the port it listens on for your session. It then sets up a reverse HTTP(S) proxy that forwards traffic back to your server, over the newly created tunnel.

## Building the project

You can use the `docker` image, or you can build it.

To build the project, you will need to have [golang](https://go.dev) installed. You can then run the following command:

```bash
go install github.com/gabriel-samfira/localshow/cmd/localshowd@latest
```

Copy the binary somewhere in your path:

```bash
sudo cp $(go env GOPATH)/bin/localshowd /usr/local/bin/localshowd
```

## Configuring the server

The configuration file is a simple `toml`:

```toml
[ssh_server]
# This is the SSH server bind address.
bind_address = "0.0.0.0"
# This is the SSH server listen port. Feel free to use port 22 here,
# after you set the cap_net_bind_service=+ep capability on the binary.
bind_port = 2022

# this is the SSH server host certificate. If it does not exist,
# it will be created on startup.
# Please generate a proper one and secure it.
host_key_path = "/tmp/testcert"
authorized_keys_path = "/home/tun/.ssh/authorized_keys"
disable_auth = false

[http_server]
bind_address = "0.0.0.0"
# The HTTP reverse proxy bind port. Feel free to use port 80 here,
# after you set the cap_net_bind_service=+ep capability on the binary.
bind_port = 9898
# The TLS bind port. Feel free to use port 443 here, after you set the
# cap_net_bind_service=+ep capability on the binary.
# This option is ignored if use_tls is set to false.
tls_bind_port = 9899
# Exclude a list of subdomains from localshow allocation. If a user will
# try to allocate a subdomain that matches one of the excluded subdomains,
# the allocation will fail.
excluded_subdomains = ["", "www", "email"]
# The base domain name used by localshow to create virtual hosts. Subdomains
# will be allocated under this domain name.
domain_name = "localshow.example.com"
# Enable the TLS listener.
use_tls = true
    [http_server.tls]
    # The x509 certificates used here should be valid for wildcard domains
    # and MUST match the domain set in domain_name
    #
    # Example: *.localshow.example.com
    #
    # The certificate needs to be concatenated with the full chain.
    # These options are ignored if use_tls is set to false.
    certificate = "/etc/localshow/localshow.example.com/certificate.pem"
    key = "/etc/localshow/localshow.example.com/privkey1.pem"

# This section enables and configures the golang debug server. You can use it for
# debug and profiling. I encourage you to only use it when needed and to only bind
# it to localhost.
[debug_server]
enabled = false
bind_address = "127.0.0.1"
bind_port = 6060
```

Read the comments in the config sample to understand what each option does.

## Configuring the server

Create a config dir:

```bash
sudo mkdir -p /etc/localshow/
```

Copy the sample config file and edit it:

```bash
sudo cp testdata/config.toml /etc/localshow/config.toml
```

## Running the server using Docker/Podman

At this point you can either use the `docker` image or run the binary directly.

Using docker, you can simply run:

```bash
docker run \
    --restart=always -d \
    -v /etc/localshow:/etc/localshow:z \
    -p 22:22 -p 80:80 -p 443:443 \
    --name localshow \
    ghcr.io/gabriel-samfira/localshow:v0.1
```

You will need to adjust the ports you expose to match the ports you set in the config file. If you've configured port `22` for the SSH service in the config, you must expose port `22` in the docker run command. The same is true for the HTTP and HTTPS ports.

## Running the server using systemd

If you want to run it as a `systemd` service, you can use the following instructions:

Create a user under which the service will run:

```bash
sudo useradd --shell /usr/bin/false \
      --system \
      --no-create-home localshow
```

Change owner on the config dir:

```bash
sudo chown -R localshow:localshow /etc/localshow
```

Enable binding to privileged ports:

```bash
sudo setcap cap_net_bind_service=+ep /usr/local/bin/localshowd
```

This will allow you to bind to `80`, `443` and `22` without running the server as root. Note, you will have to apply this flag every time you update the binary.

Copy the sample systemd service file and enable the service:

```bash
sudo cp contrib/localshowd.service /etc/systemd/system/localshowd.service
sudo systemctl daemon-reload
sudo systemctl enable localshowd.service
sudo systemctl start localshowd.service
```

## Using the service

Now that the service is up, you can expose your local webserver to the internet:

```bash
ssh -R 80:localhost:8080 example.com -p 2022
```

Once connected, you will receive a banner with the created tunnel:

```bash
root@gitea:~# ssh -R 80:localhost:3000 example.com -p 2022

### 
### HTTP tunnel successfully created on http://starchy-unit.localshow.example.com:9898
### HTTPS tunnel successfully created on https://starchy-unit.localshow.example.com:9899
###

```

If you disable TLS, you will only get the HTTP tunnel.

You can also request a user defined subdomain:

```bash
root@gitea:~# ssh -R gitea:80:localhost:3000 example.com -p 2022

### 
### HTTP tunnel successfully created on http://gitea.localshow.example.com:9898
### HTTPS tunnel successfully created on https://gitea.localshow.example.com:9899
###

```

Have fun!
