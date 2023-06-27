# dqlite distributed cluster implementation example

Built with https://github.com/canonical/go-dqlite

This example works inside single k8s cluster only.

Start with 1 replica, but if you need HA dqlite cluster, scale up to 3 replicas at least.

## test-app.go

Go application example with demonstration dqlite work

## test-deployment.yml

Kubernetes StatefulSet, Namespace, ServiceAccount, Role, RoleBinding

## Dockerfile

Dockerfile to build image with dqlite example app

```bash
docker build -t go-k8s-test-app .
docker tag go-k8s-test-app <docker-registry>:<port>/go-k8s-test-app:registry
docker push <docker-registry>:<port>/go-k8s-test-app:registry
```

## Testing dqlite cluster DB work on one of the nodes with builtin dqlite utility

Deployment scaled to 5 replicas to test cluster

```bash
root@list-svc-5995f94cd7-zqr6d:/app# ls -lah db/
total 25M
drwxr-xr-x 2 root root 4.0K Jun 24 14:22 .
drwxr-xr-x 1 root root 4.0K Jun 24 14:14 ..
-rw------- 1 root root  322 Jun 24 14:22 cluster.yaml
-rw------- 1 root root   58 Jun 24 14:14 info.yaml
-rw------- 1 root root   32 Jun 24 14:14 metadata1
-rw------- 1 root root 8.0M Jun 24 14:19 open-1
-rw------- 1 root root 8.0M Jun 24 14:14 open-2
-rw------- 1 root root 8.0M Jun 24 14:14 open-3
```

```bash
root@list-svc-5995f94cd7-zqr6d:/app# cat db/cluster.yaml
- ID: 3297041220608546238
  Address: 10.1.204.67:9001
  Role: 0
- ID: 5520441778466322099
  Address: 10.1.204.89:9001
  Role: 0
- ID: 7491936048388722374
  Address: 10.1.204.113:9001
  Role: 0
- ID: 6796550988536159816
  Address: 10.1.204.75:9001
  Role: 1
- ID: 13463261731249424942
  Address: 10.1.204.88:9001
  Role: 1
```

```bash
root@list-svc-5995f94cd7-zqr6d:/app# dqlite -s 10.1.204.67:9001 test-db
dqlite> select * from model;
test-key|test value
dqlite> insert into model(key,value) values("new-key","new value");
dqlite> select * from model;
test-key|test value
new-key|new value
```
