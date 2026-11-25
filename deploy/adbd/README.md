# The adb server sidecar

`cadence-v2`'s device updates reach android boards over adb without our code ever running an adb
binary: the server speaks the adb server protocol through
[gadb](https://github.com/electricbubble/gadb) (pure Go, MIT) against this sidecar, which owns the
key the devices already trust. Without it (`CADENCE_ADB_SERVER=off`, or the container absent) the
adb transport refuses and the link and ssh transports carry updates alone.

```sh
docker build -f deploy/adbd/Dockerfile -t <registry>/cadence-adbd:1 .
docker buildx build --platform linux/amd64 -f deploy/adbd/Dockerfile \
  -t <registry>/cadence-adbd:1 --push .
```

The key: generate one pair (or copy an `~/.android/adbkey` your devices already authorize) into a
secret the sidecar reads, so no device shows a prompt:

```sh
kubectl -n <namespace> create secret generic cadence-adb-key \
  --from-file=adbkey=$HOME/.android/adbkey
```

The manifest block: a second container in the `cadence-v2` pod, sharing its network namespace, so
the server reaches it at `localhost:5037`:

```yaml
        - name: adbd
          image: <registry>/cadence-adbd:1
          env:
            - {name: ADB_VENDOR_KEYS, value: /keys/adbkey}
            - {name: HOME, value: /tmp}
          volumeMounts:
            - {name: adb-keys, mountPath: /keys, readOnly: true}
          securityContext:
            {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]},
             runAsNonRoot: true, runAsUser: 65534, seccompProfile: {type: RuntimeDefault}}
      # under the deployment's volumes:
        - name: adb-keys
          secret: {secretName: cadence-adb-key}
```

`adb -a nodaemon server` runs the server in the foreground on every interface; only the pod's
network namespace hears it. The devices must be adb-reachable from the pod (`<device-ip>:5555`;
check from a pod on the server's node).
