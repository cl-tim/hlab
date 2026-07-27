# Enrolment test target

A throwaway Ubuntu container running sshd with password authentication and a
sudo-capable account, used by the enrolment integration tests. The bootstrap
script's behaviour — `useradd`, `visudo` validation, `authorized_keys`
permissions, idempotent re-runs — cannot be verified without a real system.

```bash
docker build -t hlab-enroll-test .
docker run -d --name hlab-enroll -p 52322:22 hlab-enroll-test

HLAB_TEST_SSH_ADDR=127.0.0.1:52322 \
HLAB_TEST_SSH_USER=labadmin \
HLAB_TEST_SSH_PASSWORD=testpass123 \
  go test ./internal/enroll/ -run Integration -v

docker rm -f hlab-enroll
```

The credentials here are deliberately throwaway and only ever reachable on
localhost.
