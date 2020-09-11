package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	EnvLogtubeAutoMapping = "LOGTUBE_K8S_AUTO_MAPPING"

	HostPathLogtubeCollectLogsPrefix = "/data/logtube-logs"
)

var (
	optDryRun, _ = strconv.ParseBool(os.Getenv("AUTOMAPPING_DRY_RUN"))
)

type WorkloadPatch struct {
	Spec struct {
		Template corev1.PodTemplateSpec `json:"template"`
	} `json:"spec"`
}

func updateVolumeMounts(cfg *rest.Config, client *kubernetes.Clientset, pod corev1.Pod, wp *WorkloadPatch) (err error) {
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

func buildLogger(dp string) func(s string) {
	sb := &strings.Builder{}
	sb.WriteString("└ deployment: [")
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
			dpLog := buildLogger(dp.Name)
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
				dpLog("status.replicas == 0")
				continue
			}
			// check selector
			if len(dp.Spec.Selector.MatchLabels) == 0 {
				dpLog("no selector found")
				continue
			}
			// list pods
			var podList *corev1.PodList
			if podList, err = client.CoreV1().Pods(dp.Namespace).List(
				context.Background(),
				metav1.ListOptions{LabelSelector: buildSelector(dp.Spec.Selector.MatchLabels)},
			); err != nil {
				return
			}
			if len(podList.Items) == 0 {
				dpLog("no pods found")
				continue
			}
			var wp WorkloadPatch
			// build patch volumes
			hostPathType := corev1.HostPathDirectoryOrCreate
			wp.Spec.Template.Spec.Volumes = []corev1.Volume{
				{Name: VolumeNameLogtubeAutoMapping, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: HostPathLogtubeCollectLogsPrefix + "/" + dp.Namespace + "-" + dp.Name,
					Type: &hostPathType,
				}}},
			}
			// build patch volumeMounts
			if err = updateVolumeMounts(cfg, client, podList.Items[0], &wp); err != nil {
				return
			}
			if len(wp.Spec.Template.Spec.Containers) == 0 {
				dpLog("no container volume mount updated")
				return
			}
			sPatch, _ := json.Marshal(wp)
			dpLog("will patch: " + string(sPatch))
		}
	}
}
