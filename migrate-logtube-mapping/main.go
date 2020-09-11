package main

import (
	"context"
	"encoding/json"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"log"
	"os"
	"strconv"
	"strings"
)

var (
	optDryRun, _ = strconv.ParseBool(os.Getenv("MIGRATE_LOGTUBE_MAPPING_DRY_RUN"))
)

const (
	LegacyHostPath = "filebeat-collect-logs"
)

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

		for _, wl := range dpList.Items {
			log.Printf("deployment: [%s]", wl.Name)

			var volumeIndexes []int
			var volumeNames []string

			for vi, v := range wl.Spec.Template.Spec.Volumes {
				if v.HostPath != nil {
					if strings.Contains(v.HostPath.Path, LegacyHostPath) {
						volumeIndexes = append(volumeIndexes, vi)
						volumeNames = append(volumeNames, v.Name)
					}
				}
			}

			var ops []map[string]interface{}
			for _, vi := range volumeIndexes {
				ops = append(ops, map[string]interface{}{
					"op":   "remove",
					"path": fmt.Sprintf("/spec/template/spec/volumes/%d", vi),
				})
			}

			for ci, c := range wl.Spec.Template.Spec.Containers {
				for vmi, vm := range c.VolumeMounts {
					for _, vn := range volumeNames {
						if vm.Name == vn {
							ops = append(ops, map[string]interface{}{
								"op":   "remove",
								"path": fmt.Sprintf("/spec/template/spec/containers/%d/volumeMounts/%d", ci, vmi),
							})
							ops = append(ops, map[string]interface{}{
								"op":   "add",
								"path": fmt.Sprintf("/spec/template/spec/containers/%d/env/-", ci),
								"value": map[string]interface{}{
									"name":  "LOGTUBE_K8S_AUTO_MAPPING",
									"value": vm.MountPath,
								},
							})
							break
						}
					}
				}
			}

			if len(ops) == 0 {
				continue
			}

			var buf []byte
			if buf, err = json.Marshal(ops); err != nil {
				return
			}

			log.Println(string(buf))

			if !optDryRun {
				if _, err = client.AppsV1().Deployments(wl.Namespace).Patch(context.Background(), wl.Namespace, types.JSONPatchType, buf, metav1.PatchOptions{}); err != nil {
					return
				}
			}

			// TODO: remove safe belt
			break
		}

		// TODO: remove safe belt
		break
	}
}
