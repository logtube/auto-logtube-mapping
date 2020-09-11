package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	AnnotationAutoLogtubeMappingEnabled = "io.github.logtube.auto-mapping/enabled"
)

var (
	candidateLogPaths = []string{
		"/work/logs",
		"/app/logs",
		"/usr/local/tomcat/logs",
		"/usr/local/logs",
		"/var/www/logs",
		"/var/www/log",
	}
	buildCandidateLogPathCheckScript = func() io.Reader {
		sb := &strings.Builder{}
		for _, cp := range candidateLogPaths {
			sb.WriteString(fmt.Sprintf(`if [ -d %s ]; then echo %s; fi;`, cp, cp))
		}
		return strings.NewReader(sb.String())
	}
)

var (
	optDryRun, _ = strconv.ParseBool(os.Getenv("AUTOMAPPING_DRY_RUN"))
)

func determinePodLogPath(cfg *rest.Config, client *kubernetes.Clientset, wlName string, pod corev1.Pod) (logPath string, err error) {
	if len(pod.Spec.Containers) == 0 {
		err = errors.New("wired, I see no containers in Pod")
		return
	}
	var container string
	for _, c := range pod.Spec.Containers {
		if c.Name == wlName {
			container = wlName
			break
		}
	}
	if container == "" {
		container = pod.Spec.Containers[0].Name
	}
	// execute
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: container,
		Command:   []string{"sh"},
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)
	log.Println(req.URL())
	var exec remotecommand.Executor
	if exec, err = remotecommand.NewSPDYExecutor(cfg, "POST", req.URL()); err != nil {
		return
	}
	out := &bytes.Buffer{}
	if err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  buildCandidateLogPathCheckScript(),
		Stdout: out,
		Stderr: ioutil.Discard,
	}); err != nil {
		return
	}
	outSplits := strings.Split(out.String(), "\n")
	if len(outSplits) == 0 {
		err = errors.New("test scripts has no response")
	}
	logPath = strings.TrimSpace(outSplits[0])
	for _, cddLogPath := range candidateLogPaths {
		if cddLogPath == logPath {
			return
		}
	}
	err = errors.New("got unexpected response from test script: " + logPath)
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
			// check annotation
			if enabled, _ := strconv.ParseBool(dp.Annotations[AnnotationAutoLogtubeMappingEnabled]); !enabled {
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
			// build selector
			selector := buildSelector(dp.Spec.Selector.MatchLabels)
			// list pods
			var podList *corev1.PodList
			if podList, err = client.CoreV1().Pods(dp.Namespace).List(context.Background(), metav1.ListOptions{LabelSelector: selector}); err != nil {
				return
			}
			if len(podList.Items) == 0 {
				dpLog("no pods found")
				continue
			}
			// determine log path
			var logPath string
			if logPath, err = determinePodLogPath(cfg, client, dp.Name, podList.Items[0]); err != nil {
				log.Printf("%+v", err)
				return
			}
			dpLog("found log path: " + logPath)
		}
	}
}
