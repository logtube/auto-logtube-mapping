# auto-logtube-mapping

针对 Kubernetes 集群的自动 Logtube 日志目录映射

**注意该工具属于个人专属工具，不要贸然使用**

## 如何使用

1. 创建命名空间 `autoops`

2. 部署 `auto-logtube-mapping`

```yaml
# 在 autoops 命名空间创建专用的 ServiceAccount
apiVersion: v1
kind: ServiceAccount
metadata:
  name: auto-logtube-mapping
  namespace: autoops
---
# 创建 ClusterRole auto-logtube-mapping
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRole
metadata:
  name: auto-logtube-mapping
rules:
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: ["apps"]
    resources: ["deployments","statefulsets"]
    verbs: ["list", "patch"]
---
# 创建 ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
kind: ClusterRoleBinding
metadata:
  name: auto-logtube-mapping
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: auto-logtube-mapping
subjects:
  - kind: ServiceAccount
    name: auto-logtube-mapping
    namespace: autoops
---
# 创建 CronJob
apiVersion: batch/v1beta1
kind: CronJob
metadata:
  name: auto-logtube-mapping
  namespace: autoops
spec:
  schedule: "5 2 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccount: auto-logtube-mapping
          containers:
            - name: auto-logtube-mapping
              image: guoyk/auto-logtube-mapping
              env:
                - name: LOGTUBE_LOGS_HOST_PATH
                  # 注意，此处需要指定主机专门规划的日志存储目录
                  value: /data/logtube-logs
          restartPolicy: OnFailure
```

3. 为 `Deployment` 或者 `StatefulSet` 添加注解

```yaml
apiVersion: apps/v1
kind: Deployment
annotations:
    io.github.logtube.auto-mapping/enabled: "true"
# ....
```

4. 在容器内，使用 Dockerfile，或者是 Kubernetes 设置环境变量

`LOGTUBE_K8S_AUTO_MAPPING=/work/logs`

## 许可证

Guo Y.K., MIT License
