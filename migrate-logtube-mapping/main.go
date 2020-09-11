package main

import (
	"context"
	"encoding/json"
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

type ContainerPatch struct {
	Name         string                   `json:"name"`
	Env          []map[string]interface{} `json:"env,omitempty"`
	VolumeMounts []map[string]interface{} `json:"volumeMounts,omitempty"`
}

type Patch struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []ContainerPatch         `json:"containers,omitempty"`
				Volumes    []map[string]interface{} `json:"volumes,omitempty"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
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

		for _, wl := range dpList.Items {
			log.Printf("deployment: [%s]", wl.Name)

			var p Patch

			var volumeNames []string

			for _, v := range wl.Spec.Template.Spec.Volumes {
				if v.HostPath != nil {
					if strings.Contains(v.HostPath.Path, LegacyHostPath) {
						// delete volume
						p.Spec.Template.Spec.Volumes = append(p.Spec.Template.Spec.Volumes, map[string]interface{}{
							"$patch": "delete",
							"name":   v.Name,
						})
						// record volume names
						volumeNames = append(volumeNames, v.Name)
					}
				}
			}

			for _, c := range wl.Spec.Template.Spec.Containers {
				var found bool
				cp := ContainerPatch{Name: c.Name}

			loopVM:
				for _, vm := range c.VolumeMounts {
					for _, vn := range volumeNames {
						if vm.Name == vn {
							cp.VolumeMounts = append(cp.VolumeMounts, map[string]interface{}{
								"$patch":    "delete",
								"mountPath": vm.MountPath,
							})

							cp.Env = append(cp.Env, map[string]interface{}{
								"name":  "LOGTUBE_K8S_AUTO_MAPPING",
								"value": vm.MountPath,
							})
							found = true
							break loopVM
						}
					}
				}

				if found {
					p.Spec.Template.Spec.Containers = append(p.Spec.Template.Spec.Containers, cp)
				}
			}

			if len(p.Spec.Template.Spec.Containers) > 0 && len(p.Spec.Template.Spec.Volumes) == 0 {
				continue
			}

			var buf []byte
			if buf, err = json.Marshal(p); err != nil {
				return
			}

			log.Println(string(buf))

			if !optDryRun {
				if _, err = client.AppsV1().Deployments(wl.Namespace).Patch(context.Background(), wl.Name, types.StrategicMergePatchType, buf, metav1.PatchOptions{}); err != nil {
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
