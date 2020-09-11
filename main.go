package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/deprecated/scheme"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"log"
	"os"
	"strconv"
	"strings"
)

const (
	AnnotationLogtubeAutoMappingEnabled = "io.github.logtube.auto-mapping/enabled"

	VolumeNameLogtubeAutoMapping = "vol-logtube-auto-mapping"

	EnvLogtubeAutoMapping  = "LOGTUBE_K8S_AUTO_MAPPING"
	EnvLogtubeLogsHostPath = "LOGTUBE_LOGS_HOST_PATH"
)

var (
	optDryRun, _ = strconv.ParseBool(os.Getenv("AUTOMAPPING_DRY_RUN"))
	optHostPath  = os.Getenv(EnvLogtubeLogsHostPath)
)

type WorkloadPatch struct {
	Spec struct {
		Template corev1.PodTemplateSpec `json:"template"`
	} `json:"spec"`

	namespace string
	name      string
}

func newWorkloadPatch(namespace, name string) *WorkloadPatch {
	var wp WorkloadPatch
	hostPathType := corev1.HostPathDirectoryOrCreate
	wp.namespace = namespace
	wp.name = name
	wp.Spec.Template.Spec.Volumes = []corev1.Volume{
		{Name: VolumeNameLogtubeAutoMapping, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: optHostPath + "/" + namespace + "-" + name,
			Type: &hostPathType,
		}}},
	}
	return &wp
}

func (wp *WorkloadPatch) jsonMarshal() ([]byte, error) {
	return json.Marshal(wp)
}

func (wp *WorkloadPatch) updateVolumeMounts(cfg *rest.Config, client *kubernetes.Clientset, selectorLabels map[string]string) (err error) {
	// check selectorLabels
	if len(selectorLabels) == 0 {
		err = fmt.Errorf("%s/%s: no selector labels", wp.namespace, wp.name)
		return
	}
	// list pods
	var podList *corev1.PodList
	if podList, err = client.CoreV1().Pods(wp.namespace).List(
		context.Background(),
		metav1.ListOptions{LabelSelector: buildSelector(selectorLabels)},
	); err != nil {
		return
	}
	if len(podList.Items) == 0 {
		err = fmt.Errorf("%s/%s: no pods", wp.namespace, wp.name)
		return
	}
	// one pod
	pod := podList.Items[0]
	for _, container := range pod.Spec.Containers {
		// execute
		req := client.CoreV1().RESTClient().Post().
			Resource("pods").
			Name(pod.Name).
			Namespace(pod.Namespace).
			SubResource("exec")
		req.VersionedParams(&corev1.PodExecOptions{
			Container: container.Name,
			Command:   []string{"sh"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
		var exec remotecommand.Executor
		if exec, err = remotecommand.NewSPDYExecutor(cfg, "POST", req.URL()); err != nil {
			return
		}
		out := &bytes.Buffer{}
		if err = exec.Stream(remotecommand.StreamOptions{
			Stdin:  strings.NewReader(fmt.Sprintf("echo ${%s}", EnvLogtubeAutoMapping)),
			Stdout: out,
			Stderr: ioutil.Discard,
		}); err != nil {
			return
		}
		logPath := strings.TrimSpace(out.String())
		if logPath == "" {
			continue
		}
		wp.Spec.Template.Spec.Containers = append(wp.Spec.Template.Spec.Containers, corev1.Container{
			Name: container.Name,
			VolumeMounts: []corev1.VolumeMount{
				{MountPath: logPath, Name: VolumeNameLogtubeAutoMapping},
			},
		})
		return
	}
	if len(wp.Spec.Template.Spec.Containers) == 0 {
		err = errors.New("no volume mounts updated")
		return
	}
	return
}

func buildSelector(m map[string]string) string {
	sb := &strings.Builder{}
	for k, v := range m {
		if sb.Len() > 0 {
			sb.WriteRune(',')
		}
		sb.WriteString(k)
		sb.WriteRune('=')
		sb.WriteString(v)
	}
	return sb.String()
}

func buildLoggerWhitespaces(l int) string {
	if l < 24 {
		return strings.Repeat("-", 24-l)
	} else if l < 48 {
		return strings.Repeat("-", 48-l)
	} else {
		return ""
	}
}

func buildLogger(key string, dp string) func(s string) {
	sb := &strings.Builder{}
	sb.WriteString("└ ")
	sb.WriteString(key)
	sb.WriteString(": [")
	sb.WriteString(dp)
	sb.WriteString("] ")
	sb.WriteString(buildLoggerWhitespaces(len(dp)))
	sb.WriteRune(' ')
	h := sb.String()
	return func(s string) {
		log.Println(h + s)
	}
}

func exit(err *error) {
	if *err != nil {
		log.Println("exited with error:", (*err).Error())
		os.Exit(1)
	} else {
		log.Println("exited")
	}
}

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	if optDryRun {
		log.SetPrefix("(dry) ")
	}

	var err error
	defer exit(&err)

	if optHostPath == "" {
		err = errors.New("missing environment variable: " + EnvLogtubeLogsHostPath)
		return
	}

	var cfg *rest.Config
	if cfg, err = rest.InClusterConfig(); err != nil {
		return
	}
	var client *kubernetes.Clientset
	if client, err = kubernetes.NewForConfig(cfg); err != nil {
		return
	}

	var nsList *corev1.NamespaceList
	if nsList, err = client.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{}); err != nil {
		return
	}

	for _, ns := range nsList.Items {
		log.Printf("namespace: [%s]", ns.Name)

		var dpList *appsv1.DeploymentList
		if dpList, err = client.AppsV1().Deployments(ns.Name).List(context.Background(), metav1.ListOptions{}); err != nil {
			return
		}

		for _, dp := range dpList.Items {
			scopeLog := buildLogger("deployment", dp.Name)
			// check annotations exists
			if dp.Annotations == nil {
				continue
			}
			// check enabled
			if enabled, _ := strconv.ParseBool(dp.Annotations[AnnotationLogtubeAutoMappingEnabled]); !enabled {
				continue
			}
			// check status.replicas
			if dp.Status.Replicas == 0 {
				scopeLog("status.replicas == 0")
				continue
			}
			wp := newWorkloadPatch(dp.Namespace, dp.Name)
			if err = wp.updateVolumeMounts(cfg, client, dp.Spec.Selector.MatchLabels); err != nil {
				scopeLog("failed to update volume mounts: " + err.Error())
				err = nil
				continue
			}
			var patch []byte
			if patch, err = wp.jsonMarshal(); err != nil {
				return
			}
			// execute patch
			if !optDryRun {
				if _, err = client.AppsV1().Deployments(dp.Namespace).Patch(context.Background(), dp.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
					return
				}
			}
			scopeLog("patched")
		}

		var stList *appsv1.StatefulSetList
		if stList, err = client.AppsV1().StatefulSets(ns.Name).List(context.Background(), metav1.ListOptions{}); err != nil {
			return
		}

		for _, st := range stList.Items {
			scopeLog := buildLogger("statefulset", st.Name)
			// check annotations exists
			if st.Annotations == nil {
				continue
			}
			// check enabled
			if enabled, _ := strconv.ParseBool(st.Annotations[AnnotationLogtubeAutoMappingEnabled]); !enabled {
				continue
			}
			// check status.replicas
			if st.Status.Replicas == 0 {
				scopeLog("status.replicas == 0")
				continue
			}
			wp := newWorkloadPatch(st.Namespace, st.Name)
			if err = wp.updateVolumeMounts(cfg, client, st.Spec.Selector.MatchLabels); err != nil {
				scopeLog("failed to update volume mounts: " + err.Error())
				err = nil
				continue
			}
			var patch []byte
			if patch, err = wp.jsonMarshal(); err != nil {
				return
			}
			// execute patch
			if !optDryRun {
				if _, err = client.AppsV1().StatefulSets(st.Namespace).Patch(context.Background(), st.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
					return
				}
			}
			scopeLog("patched")
		}
	}
}
