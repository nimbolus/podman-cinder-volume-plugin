# podman-cinder-volume-plugin

This Podman volume plugin makes OpenStack Block Storage volumes (aka Cinder volumes) available as Podman volumes.

## How to install?

Download binary:

```sh
mkdir -p /usr/local/libexec/podman
podman create --name cinder --pull always ghcr.io/nimbolus/podman-cinder-volume-plugin
podman cp cinder:/cinder /usr/local/libexec/podman/cinder
podman rm cinder
```

Create configuration file and start plugin via systemd:
```sh
curl -o /etc/podman-cinder-volume-plugin.env https://raw.githubusercontent.com/nimbolus/podman-cinder-volume-plugin/refs/heads/main/dist/config.env
chmod 0600 /etc/podman-cinder-volume-plugin.env

curl -o /etc/systemd/system/podman-cinder-volume-plugin.service https://raw.githubusercontent.com/nimbolus/podman-cinder-volume-plugin/refs/heads/main/dist/systemd.service
systemctl daemon-reload
systemctl enable --now podman-cinder-volume-plugin.service
```

Finally, register the plugin in `/etc/containers/containers.conf`:

```conf
[engine]
volume_plugin_timeout = 30

[engine.volume_plugins]
cinder = "/var/run/docker/plugins/cinder.sock"
```

## Supported env vars

Here's the list of env vars supported by this plugin:

| Name                             | Default Value | Description                                                                       |
| -------------------------------- | ------------- | --------------------------------------------------------------------------------- |
| OS_AUTH_URL                      |               | See [1].                                                                          |
| OS_USERNAME                      |               | See [1].                                                                          |
| OS_USERID                        |               | See [1].                                                                          |
| OS_PASSWORD                      |               | See [1].                                                                          |
| OS_PASSCODE                      |               | See [1].                                                                          |
| OS_TENANT_ID                     |               | See [1].                                                                          |
| OS_TENANT_NAME                   |               | See [1].                                                                          |
| OS_DOMAIN_ID                     |               | See [1].                                                                          |
| OS_DOMAIN_NAME                   |               | See [1].                                                                          |
| OS_APPLICATION_CREDENTIAL_ID     |               | See [1].                                                                          |
| OS_APPLICATION_CREDENTIAL_NAME   |               | See [1].                                                                          |
| OS_APPLICATION_CREDENTIAL_SECRET |               | See [1].                                                                          |
| OS_PROJECT_ID                    |               | See [1].                                                                          |
| OS_PROJECT_NAME                  |               | See [1].                                                                          |
| OS_REGION_NAME                   |               | Name of the OpenStack region where Compute & Block Storage resources are located. |
| DEFAULT_SIZE                     | `20`          | Default volume size in GB.                                                        |
| VOLUME_PREFIX                    |               | Name prefix of volumes managed by this plugin.                                    |
| LOG_LEVEL                        | `info`        | Log level (either: trace, debug, info, warn, error, fatal, panic).                |
| DEBUG                            |               | Enable /pprof/trace endpoint when the value is not empty.                         |

[1] https://docs.openstack.org/python-openstackclient/pike/cli/man/openstack.html#environment-variables

## Supported volume options

Here's the list of options you can pass when creating a volume :

| Name                | Default Value                       | Description                                                                             |
| ------------------- | ----------------------------------- | --------------------------------------------------------------------------------------- |
| `size`              | The value of `DEFAULT_SIZE` env var | The size of the underlying Block Storage volume.                                        |
| `availability_zone` | N/A                                 | AZ where the underlying Block Storage volume should be created.                         |
| `consistency_group` | N/A                                 | See https://docs.openstack.org/cinder/latest/admin/blockstorage-consistency-groups.html |
| `description`       | N/A                                 | Description of the underlying Block Storage volume.                                     |
| `source_snapshot`   | N/A                                 | ID of the Block Storage snaphost used to create the volume.                             |
| `source_backup`     | N/A                                 | ID of the Block Storage backup used to create the volume.                               |
| `volume_type`       | N/A                                 | Block storage volume type.                                                              |
| `uid`               | 0                                   | Default UID set on the volume root dir after formatting the volume.                     |
| `gid`               | 0                                   | Default GID set on the volume root dir after formatting the volume.                     |
| `mode`              | 0750                                | Default file mode set on the volume root dir after formatting the volume.               |

For instance, if you want to define the size of a volume and the source snapshot the volume should be created from, with Docker CLI:

```
$ podman volume create -d cinder -o size=40 -o source_snapshot=<snapshot-uuid> test
```

And with `docker-compose`:

```yaml
services:
  # ...

volumes:
  test:
    name: test
    driver: cinder
    driver_opts:
      size: 40
      source_snapshot: "<snapshot-uuid>"
```
