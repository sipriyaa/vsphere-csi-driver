/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/util/podutils"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/manifest"
	fss "k8s.io/kubernetes/test/e2e/framework/statefulset"
)

const (
	StatefulSetPollFor256DiskSupport    = 10 * time.Second
	StatefulSetTimeoutFor256DiskSupport = 30 * time.Minute
	StatefulPodTimeoutFor256DiskSupport = 80 * time.Minute
)

var statefulPodRegex = regexp.MustCompile("(.*)-([0-9]+)$")

func GetStatefulSetFromManifestFor265Disks(ns string) *appsv1.StatefulSet {
	ssManifestFilePath := filepath.Join(manifestPathFor256Disks, "statefulset.yaml")
	framework.Logf("Parsing statefulset from %v", ssManifestFilePath)
	ss, err := manifest.StatefulSetFromManifest(ssManifestFilePath, ns)
	framework.ExpectNoError(err)
	return ss
}

func CreateMultipleStatefulSetsInSameNsFor256DiskSupport(ns string, ss *appsv1.StatefulSet,
	c clientset.Interface, replicas int32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	framework.Logf(fmt.Sprintf("Creating statefulset %v/%v with %d replicas and selector %+v",
		ss.Namespace, ss.Name, replicas, ss.Spec.Selector))
	_, err := c.AppsV1().StatefulSets(ns).Create(ctx, ss, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	WaitForStsPodsToBeInRunningReadyState(c, replicas, ss)
}

func WaitForStsPodsToBeInRunningReadyState(c clientset.Interface, numStatefulPods int32, ss *appsv1.StatefulSet) {
	numPodsRunning := numStatefulPods
	numPodsReady := numStatefulPods
	{
		pollErr := wait.PollImmediate(StatefulSetPollFor256DiskSupport, StatefulSetTimeoutFor256DiskSupport,
			func() (bool, error) {
				podList := GetListOfPodsInSts(c, ss)
				fss.SortStatefulPods(podList)
				if int32(len(podList.Items)) < numPodsRunning {
					framework.Logf("Found %d stateful pods, waiting for %d", len(podList.Items), numPodsRunning)
					return false, nil
				}
				if int32(len(podList.Items)) > numPodsRunning {
					return false, fmt.Errorf("too many pods scheduled, expected %d got %d", numPodsRunning, len(podList.Items))
				}
				for _, p := range podList.Items {
					shouldBeReady := getOrdinalForMultipleStsPodsInGivenNS(&p) < int(numPodsReady)
					isReady := podutils.IsPodReady(&p)
					desiredReadiness := shouldBeReady == isReady
					framework.Logf("Waiting for pod %v to enter %v - Ready=%v, currently %v - Ready=%v", p.Name, v1.PodRunning, shouldBeReady, p.Status.Phase, isReady)
					if p.Status.Phase != v1.PodRunning || !desiredReadiness {
						return false, nil
					}
				}
				return true, nil
			})
		if pollErr != nil {
			framework.Failf("Failed waiting for pods to enter running: %v", pollErr)
		}
	}
}

func getOrdinalForMultipleStsPodsInGivenNS(pod *v1.Pod) int {
	ordinal := -1
	subMatches := statefulPodRegex.FindStringSubmatch(pod.Name)
	if len(subMatches) < 3 {
		return ordinal
	}
	if i, err := strconv.ParseInt(subMatches[2], 10, 32); err == nil {
		ordinal = int(i)
	}
	return ordinal
}

func DeleteMultipleStsInGivenNameSpace(c clientset.Interface, ns string) {
	ssList, err := c.AppsV1().StatefulSets(ns).List(context.TODO(), metav1.ListOptions{LabelSelector: labels.Everything().String()})
	framework.ExpectNoError(err)
	errList := []string{}
	for i := range ssList.Items {
		ss := &ssList.Items[i]
		var err error
		if ss, err = scaleStatefulSetPods(c, ss, 0, true); err != nil {
			errList = append(errList, fmt.Sprintf("%v", err))
		}
		fss.WaitForStatusReplicas(c, ss, 0)
		framework.Logf("Deleting statefulset %v", ss.Name)
		if err := c.AppsV1().StatefulSets(ss.Namespace).Delete(context.TODO(), ss.Name, metav1.DeleteOptions{OrphanDependents: new(bool)}); err != nil {
			errList = append(errList, fmt.Sprintf("%v", err))
		}
	}
	pvNames := sets.NewString()
	pvcPollErr := wait.PollImmediate(StatefulSetPollFor256DiskSupport, StatefulPodTimeoutFor256DiskSupport, func() (bool, error) {
		pvcList, err := c.CoreV1().PersistentVolumeClaims(ns).List(context.TODO(), metav1.ListOptions{LabelSelector: labels.Everything().String()})
		if err != nil {
			framework.Logf("WARNING: Failed to list pvcs, retrying %v", err)
			return false, nil
		}
		for _, pvc := range pvcList.Items {
			pvNames.Insert(pvc.Spec.VolumeName)
			framework.Logf("Deleting pvc: %v with volume %v", pvc.Name, pvc.Spec.VolumeName)
			if err := c.CoreV1().PersistentVolumeClaims(ns).Delete(context.TODO(), pvc.Name, metav1.DeleteOptions{}); err != nil {
				return false, nil
			}
		}
		return true, nil
	})
	if pvcPollErr != nil {
		errList = append(errList, fmt.Sprintf("Timeout waiting for pvc deletion."))
	}
	pollErr := wait.PollImmediate(StatefulSetPollFor256DiskSupport, StatefulPodTimeoutFor256DiskSupport, func() (bool, error) {
		pvList, err := c.CoreV1().PersistentVolumes().List(context.TODO(), metav1.ListOptions{LabelSelector: labels.Everything().String()})
		if err != nil {
			framework.Logf("WARNING: Failed to list pvs, retrying %v", err)
			return false, nil
		}
		waitingFor := []string{}
		for _, pv := range pvList.Items {
			if pvNames.Has(pv.Name) {
				waitingFor = append(waitingFor, fmt.Sprintf("%v: %+v", pv.Name, pv.Status))
			}
		}
		if len(waitingFor) == 0 {
			return true, nil
		}
		framework.Logf("Still waiting for pvs of statefulset to disappear:\n%v", strings.Join(waitingFor, "\n"))
		return false, nil
	})
	if pollErr != nil {
		errList = append(errList, fmt.Sprintf("Timeout waiting for pv provisioner to delete pvs, this might mean the test leaked pvs."))
	}
	if len(errList) != 0 {
		framework.ExpectNoError(fmt.Errorf("%v", strings.Join(errList, "\n")))
	}
}
