/*
	Copyright 2023 The Kubernetes Authors.

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
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/vmware/govmomi/object"
	"golang.org/x/crypto/ssh"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	fnodes "k8s.io/kubernetes/test/e2e/framework/node"
	fpod "k8s.io/kubernetes/test/e2e/framework/pod"
	fpv "k8s.io/kubernetes/test/e2e/framework/pv"
	fss "k8s.io/kubernetes/test/e2e/framework/statefulset"
	admissionapi "k8s.io/pod-security-admission/api"

	snapclient "github.com/kubernetes-csi/external-snapshotter/client/v6/clientset/versioned"
)

var _ = ginkgo.Describe("[Preferential-Topology] Preferential-Topology-Provisioning", func() {
	f := framework.NewDefaultFramework("preferential-topology-aware-provisioning")
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelPrivileged
	var (
		client    clientset.Interface
		namespace string

		topologyLength int

		preferredDatastoreChosen int

		allMasterIps []string
		masterIp     string

		dataCenters []*object.Datacenter
		clusters    []string
		//csiReplicas             int32
		//csiNamespace            string
		preferredDatastorePaths []string
		allowedTopologyRacks    []string

		sshClientConfig             *ssh.ClientConfig
		nimbusGeneratedK8sVmPwd     string
		allowedTopologies           []v1.TopologySelectorLabelRequirement
		ClusterdatastoreListVC      []map[string]string
		ClusterdatastoreListVC1     map[string]string
		ClusterdatastoreListVC2     map[string]string
		ClusterdatastoreListVC3     map[string]string
		parallelStatefulSetCreation bool
		stsReplicas                 int32
		scaleDownReplicaCount       int32
		scaleUpReplicaCount         int32
		stsScaleUp                  bool
		stsScaleDown                bool
		verifyTopologyAffinity      bool
		topValStartIndex            int
		topValEndIndex              int
		topkeyStartIndex            int
		scParameters                map[string]string
		storagePolicyInVc1Vc2       string
		allowedTopologyLen          int
		parallelPodPolicy           bool
		nodeAffinityToSet           bool
		podAntiAffinityToSet        bool
		snapc                       *snapclient.Clientset
	)

	ginkgo.BeforeEach(func() {
		var cancel context.CancelFunc
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client = f.ClientSet
		namespace = f.Namespace.Name
		multiVCbootstrap()
		sc, err := client.StorageV1().StorageClasses().Get(ctx, defaultNginxStorageClassName, metav1.GetOptions{})
		if err == nil && sc != nil {
			gomega.Expect(client.StorageV1().StorageClasses().Delete(ctx, sc.Name,
				*metav1.NewDeleteOptions(0))).NotTo(gomega.HaveOccurred())
		}
		nodeList, err := fnodes.GetReadySchedulableNodes(f.ClientSet)
		framework.ExpectNoError(err, "Unable to find ready and schedulable Node")
		if !(len(nodeList.Items) > 0) {
			framework.Failf("Unable to find ready and schedulable Node")
		}
		//csiNamespace = GetAndExpectStringEnvVar(envCSINamespace)

		nimbusGeneratedK8sVmPwd = GetAndExpectStringEnvVar(nimbusK8sVmPwd)

		sshClientConfig = &ssh.ClientConfig{
			User: "root",
			Auth: []ssh.AuthMethod{
				ssh.Password(nimbusGeneratedK8sVmPwd),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		}

		stsScaleUp = true
		stsScaleDown = true
		verifyTopologyAffinity = true
		scParameters = make(map[string]string)
		storagePolicyInVc1Vc2 = GetAndExpectStringEnvVar(envStoragePolicyNameInVC1VC2)
		topologyLength = 5
		topologyMap := GetAndExpectStringEnvVar(topologyMap)
		allowedTopologies = createAllowedTopolgies(topologyMap, topologyLength)

		//Get snapshot client using the rest config
		restConfig = getRestConfigClient()
		snapc, err = snapclient.NewForConfig(restConfig)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// csiDeployment, err := client.AppsV1().Deployments(csiNamespace).Get(
		// 	ctx, vSphereCSIControllerPodNamePrefix, metav1.GetOptions{})
		// gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// csiReplicas = *csiDeployment.Spec.Replicas

		// fetching k8s master ip
		allMasterIps = getK8sMasterIPs(ctx, client)
		masterIp = allMasterIps[0]

		// fetching datacenter details
		dataCenters, err = multiVCe2eVSphere.getAllDatacentersForMultiVC(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// fetching cluster details
		client_index := 0
		clusters, err = getTopologyLevel5ClusterGroupNames(masterIp, sshClientConfig, dataCenters, true, client_index)
		fmt.Println(clusters)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// fetching list of datastores available in different racks
		ClusterdatastoreListVC1, ClusterdatastoreListVC2,
			ClusterdatastoreListVC3, err = getListOfDatastoresByClusterName1(masterIp, sshClientConfig, clusters[0], true)
		ClusterdatastoreListVC = append(ClusterdatastoreListVC, ClusterdatastoreListVC1,
			ClusterdatastoreListVC2, ClusterdatastoreListVC3)
		fmt.Println(ClusterdatastoreListVC1)
		fmt.Println(ClusterdatastoreListVC2)
		fmt.Println(ClusterdatastoreListVC3)
		fmt.Println(ClusterdatastoreListVC)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		//set preferred datatsore time interval
		//setPreferredDatastoreTimeInterval(client, ctx, csiNamespace, namespace, csiReplicas)

	})

	ginkgo.AfterEach(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ginkgo.By(fmt.Sprintf("Deleting all statefulsets in namespace: %v", namespace))
		fss.DeleteAllStatefulSets(client, namespace)
		ginkgo.By(fmt.Sprintf("Deleting service nginx in namespace: %v", namespace))
		err := client.CoreV1().Services(namespace).Delete(ctx, servicename, *metav1.NewDeleteOptions(0))
		if !apierrors.IsNotFound(err) {
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
		framework.Logf("Perform preferred datastore tags cleanup after test completion")
		err = deleteTagCreatedForPreferredDatastore(masterIp, sshClientConfig, allowedTopologyRacks)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		framework.Logf("Recreate preferred datastore tags post cleanup")
		err = createTagForPreferredDatastore(masterIp, sshClientConfig, allowedTopologyRacks)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

	})

	/* Testcase-1:
		Add preferential tag in all the Availability zone's of VC1 and VC2  → change the preference during execution

	    Steps
	    Preferential FSS "topology-preferential-datastores" should be set and csi-vsphere-config should have
		the preferential tag.

	    1. Create SC default parameters without any topology requirement.
	    2. In each availability zone for any one datastore add preferential tag in VC1 and VC2
	    3. Create 3 statefulset with 10 replica's
	    4. Wait for all the  PVC to bound and pod's to reach running state
	    5. Verify that since the prefered datastore is available, Volume should get created on the datastores
		 which has the preferencce set
	    6. Make sure common validation points are met on PV,PVC and POD
	    7. Change the Preference in any 2 datastores
	    8. Scale up the statefulset to 15 replica
	    9. The volumes should get provision on the datastores which has the preference
	    10. Clear the data
	*/
	ginkgo.It("TagTest single preferred datastore each in VC1 and VC2 and verify it is honored", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		parallelStatefulSetCreation = true
		sts_count := 3
		stsReplicas = 2
		var dsUrls []string
		var datastoreListMap map[string]string
		scaleDownReplicaCount = 3
		scaleUpReplicaCount = 7

		scSpec := getVSphereStorageClassSpec(defaultNginxStorageClassName, nil, nil, "",
			"", false)
		sc, err := client.StorageV1().StorageClasses().Create(ctx, scSpec, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() {
			err = client.StorageV1().StorageClasses().Delete(ctx, sc.Name, *metav1.NewDeleteOptions(0))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		preferredDatastoreChosen = 1
		preferredDatastorePaths = nil

		// choose preferred datastore
		ginkgo.By("Tag preferred datastore for volume provisioning in VC1 and VC2")
		for i := 0; i < 2; i++ {
			paths, err := tagPreferredDatastore(masterIp, sshClientConfig, allowedTopologies[0].Values[i],
				preferredDatastoreChosen, ClusterdatastoreListVC[i], nil, true, i)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			preferredDatastorePaths = append(preferredDatastorePaths, paths...)

			// Get the length of the paths for the current iteration
			pathsLen := len(paths)

			for j := 0; j < pathsLen; j++ {
				// Calculate the index for ClusterdatastoreListVC based on the current iteration
				index := i + j

				if val, ok := ClusterdatastoreListVC[index][paths[j]]; ok {
					dsUrls = append(dsUrls, val)
				}
			}
		}

		framework.Logf("Waiting for %v for preferred datastore to get refreshed in the environment",
			preferredDatastoreTimeOutInterval)
		time.Sleep(preferredDatastoreTimeOutInterval)

		ginkgo.By("Create service")
		service := CreateService(namespace, client)
		defer func() {
			deleteService(namespace, client, service)
		}()

		ginkgo.By("Create 2 StatefulSet with replica count 5")
		statefulSets := createParallelStatefulSetSpec(namespace, sts_count, stsReplicas)
		var wg sync.WaitGroup
		wg.Add(sts_count)
		for i := 0; i < len(statefulSets); i++ {
			go createParallelStatefulSets(client, namespace, statefulSets[i],
				stsReplicas, &wg)

		}
		wg.Wait()

		ginkgo.By("Verify that all parallel triggered StatefulSets Pods creation should be in up and running state")
		for i := 0; i < len(statefulSets); i++ {
			fss.WaitForStatusReadyReplicas(client, statefulSets[i], stsReplicas)
			gomega.Expect(CheckMountForStsPods(client, statefulSets[i], mountPath)).NotTo(gomega.HaveOccurred())

			ssPods := GetListOfPodsInSts(client, statefulSets[i])
			gomega.Expect(ssPods.Items).NotTo(gomega.BeEmpty(),
				fmt.Sprintf("Unable to get list of Pods from the Statefulset: %v", statefulSets[i].Name))
			gomega.Expect(len(ssPods.Items) == int(stsReplicas)).To(gomega.BeTrue(),
				"Number of Pods in the statefulset should match with number of replicas")
		}
		defer func() {
			deleteAllStatefulSetAndPVs(client, namespace)
		}()

		ginkgo.By("Verify PV node affinity and that the PODS are running on appropriate node")
		for i := 0; i < len(statefulSets); i++ {
			verifyPVnodeAffinityAndPODnodedetailsForStatefulsetsLevel5(ctx, client, statefulSets[i],
				namespace, allowedTopologies, parallelStatefulSetCreation, true)
		}

		ginkgo.By("Verify volume is provisioned on the preferred datatsore")
		for i := 0; i < len(statefulSets); i++ {
			err = verifyVolumeProvisioningForStatefulSet(ctx, client, statefulSets[i], namespace,
				preferredDatastorePaths, datastoreListMap, true, true, true, dsUrls)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		ginkgo.By("Remove preferred datatsore tag which is chosen for volume provisioning")
		for i := 0; i < len(preferredDatastorePaths); i++ {
			err = detachTagCreatedOnPreferredDatastore(masterIp, sshClientConfig, preferredDatastorePaths[i],
				allowedTopologies[0].Values[i], true, i)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		var preferredDatastorePathsNew []string
		ginkgo.By("Tag new preferred datastore for volume provisioning in VC1 and VC2")
		for i := 0; i < 2; i++ {
			paths, err := tagPreferredDatastore(masterIp, sshClientConfig, allowedTopologies[0].Values[i],
				preferredDatastoreChosen, ClusterdatastoreListVC[i], preferredDatastorePaths, true, i)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			preferredDatastorePathsNew = append(preferredDatastorePathsNew, paths...)

			// Get the length of the paths for the current iteration
			pathsLen := len(paths)

			for j := 0; j < pathsLen; j++ {
				// Calculate the index for ClusterdatastoreListVC based on the current iteration
				index := i + j

				if val, ok := ClusterdatastoreListVC[index][paths[j]]; ok {
					dsUrls = append(dsUrls, val)
				}
			}
		}

		preferredDatastorePaths = append(preferredDatastorePaths, preferredDatastorePathsNew...)
		defer func() {
			ginkgo.By("Remove preferred datastore tag")
			for i := 0; i < len(preferredDatastorePathsNew); i++ {
				err = detachTagCreatedOnPreferredDatastore(masterIp, sshClientConfig, preferredDatastorePathsNew[i],
					allowedTopologies[0].Values[i], true, i)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			}
		}()

		ginkgo.By("Perform scaleup/scaledown operation on statefulsets and " +
			"verify pv affinity and pod affinity")
		for i := 0; i < len(statefulSets); i++ {
			performScalingOnStatefulSetAndVerifyPvNodeAffinity(ctx, client, scaleUpReplicaCount,
				scaleDownReplicaCount, statefulSets[i], parallelStatefulSetCreation, namespace,
				allowedTopologies, stsScaleUp, stsScaleDown, verifyTopologyAffinity)
		}

		ginkgo.By("Verify volume is provisioned on the preferred datatsore")
		for i := 0; i < len(statefulSets); i++ {
			err = verifyVolumeProvisioningForStatefulSet(ctx, client, statefulSets[i], namespace,
				preferredDatastorePaths, datastoreListMap, true, true, true, dsUrls)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	})

	/* Testcase-2:
			Create SC with storage policy available in VC1 and VC2 , set the preference in VC1 datastore only

		    Steps
			Preferential FSS "topology-preferential-datastores" should be set and csi-vsphere-config should have the preferential tag.
			set the default sync value to 1 min , so that No need to restart the csi-driver

	    1. Create storage policy with same name on both VC1 and VC2
	    2. Add preference tag on the datastore which is on VC1 only
	    3. Create statefulset using the above policy
	    4. Since the preference tag is added in VC1, volume provisioning should  happned on VC1's datastore only
		[no, first preference will be given to Storage Policy mentioned in the Storage Class]
	    5. Make sure common validation points are met on PV,PVC and POD
	    6. Reboot VC1
	    7. Scale up the stateful set to replica 15  → What should be the behaviour here
	    8. Since the VC1 is presently in reboot state, new volumes should start coming up on Vc2 .
		Once VC1  is up , Again the datastore preference should take preference
		[no, until all VCs comes up PVc provision will be stuck in Pending state]
	    9. Verify the node affinity on all PV's
	    10. Make sure POD has come up on appropriate nodes .
	    11. Clean up the data
	*/

	ginkgo.It("Tag22Test single preferred datastore each in VC1 and VC2 and verify it is honored", func() {

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		/* here we are considering storage policy of VC1 and the allowed topology is k8s-zone -> zone-1
		in case of 2-VC setup and 3-VC setup
		*/

		stsReplicas = 5
		scParameters[scParamStoragePolicyName] = storagePolicyInVc1Vc2
		topValStartIndex = 0
		topValEndIndex = 2
		scaleUpReplicaCount = 15
		stsScaleDown = false
		var dsUrls []string
		scaleDownReplicaCount = 3
		scaleUpReplicaCount = 7
		var multiVcClientIndex = 0
		var datastoreListMap map[string]string

		ginkgo.By("Set specific allowed topology")
		allowedTopologies = setSpecificAllowedTopology(allowedTopologies, topkeyStartIndex, topValStartIndex,
			topValEndIndex)

		ginkgo.By("Create StorageClass with storage policy specified")
		scSpec := getVSphereStorageClassSpec(defaultNginxStorageClassName, scParameters, nil, "",
			"", false)
		sc, err := client.StorageV1().StorageClasses().Create(ctx, scSpec, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() {
			err = client.StorageV1().StorageClasses().Delete(ctx, sc.Name, *metav1.NewDeleteOptions(0))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		preferredDatastoreChosen = 1
		preferredDatastorePaths = nil

		// choose preferred datastore
		ginkgo.By("Tag preferred datastore for volume provisioning in VC1")
		preferredDatastorePaths, err := tagPreferredDatastore(masterIp, sshClientConfig,
			allowedTopologies[0].Values[0],
			preferredDatastoreChosen, ClusterdatastoreListVC[0], nil, true, multiVcClientIndex)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pathsLen := len(preferredDatastorePaths)
		for j := 0; j < pathsLen; j++ {
			if val, ok := ClusterdatastoreListVC[0][preferredDatastorePaths[j]]; ok {
				dsUrls = append(dsUrls, val)
			}
		}

		framework.Logf("Waiting for %v for preferred datastore to get refreshed in the environment",
			preferredDatastoreTimeOutInterval)
		time.Sleep(preferredDatastoreTimeOutInterval)

		ginkgo.By("Create StatefulSet and verify pv affinity and pod affinity details")
		service, statefulset := createStafeulSetAndVerifyPVAndPodNodeAffinty(ctx, client, namespace,
			parallelPodPolicy, stsReplicas, nodeAffinityToSet, allowedTopologies, allowedTopologyLen,
			podAntiAffinityToSet, parallelStatefulSetCreation)
		defer func() {
			fss.DeleteAllStatefulSets(client, namespace)
			deleteService(namespace, client, service)
		}()

		ginkgo.By("Verify volume is provisioned on the preferred datatsore")
		err = verifyVolumeProvisioningForStatefulSet(ctx, client, statefulset, namespace,
			preferredDatastorePaths, datastoreListMap, true, true, true, dsUrls)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Rebooting VC")
		vCenterHostname := strings.Split(multiVCe2eVSphere.multivcConfig.Global.VCenterHostname, ",")
		vcAddress := vCenterHostname[0] + ":" + sshdPort
		framework.Logf("vcAddress - %s ", vcAddress)
		err = invokeVCenterReboot(vcAddress)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = waitForHostToBeUp(vCenterHostname[0])
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		ginkgo.By("Done with reboot")

		ginkgo.By("Perform scaleup/scaledown operation on statefulsets and " +
			"verify pv affinity and pod affinity")
		performScalingOnStatefulSetAndVerifyPvNodeAffinity(ctx, client, scaleUpReplicaCount,
			scaleDownReplicaCount, statefulset, parallelStatefulSetCreation, namespace,
			allowedTopologies, stsScaleUp, stsScaleDown, verifyTopologyAffinity)

		essentialServices := []string{spsServiceName, vsanhealthServiceName, vpxdServiceName}
		checkVcenterServicesRunning(ctx, vcAddress, essentialServices)

		//After reboot
		multiVCbootstrap()

		ginkgo.By("Verify volume is provisioned on the preferred datatsore")
		err = verifyVolumeProvisioningForStatefulSet(ctx, client, statefulset, namespace,
			preferredDatastorePaths, datastoreListMap, true, true, true, dsUrls)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	})

	/* Testcase-3:
	Create/Restore Snapshot of PVC single datastore preference

	Steps
	Preferential FSS "topology-preferential-datastores" should be set and csi-vsphere-config should have the
	preferential tag

	    1. Assign preferential tag to any one datastore under any one VC
	    2. Create SC with allowed topology set to all the volumes
	    3. Create PVC-1 with the above SC
	    4. Wait for PVC-1 to reach Bound state.
	    5. Describe PV-1 and verify node affinity details
	    6. Verify volume should be provisioned on the selected preferred datastore
	    7. Create SnapshotClass, Snapshot of PVC-1.
	    8. Verify snapshot state. It should be in ready-to-use state.
	    9. Verify snapshot should be created on the preferred datastore.
	    10. Restore snapshot to create PVC-2
	    11. Wait for PVC-2 to reach Bound state.
	    12. Describe PV-2 and verify node affinity details
	    13. Verify volume should be provisioned on the selected preferred datastore
	    14. Create Pod from restored PVC-2.
	    15. Make sure common validation points are met on PV,PVC and POD
	    16. Make sure POD is running on the same node as mentioned in the node affinity details.
	    17. Perform Cleanup. Delete Snapshot, Pod, PVC, SC
	    18. Remove datastore preference tags as part of cleanup.
	*/

	ginkgo.It("Testcase3Create restore snapshot of pvc using single datastore preference", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		preferredDatastoreChosen = 1
		preferredDatastorePaths = nil
		var dsUrls []string
		var multiVcClientIndex = 2

		// choose preferred datastore
		ginkgo.By("Tag preferred datastore for volume provisioning in VC3")
		preferredDatastorePaths, err := tagPreferredDatastore(masterIp, sshClientConfig,
			allowedTopologies[0].Values[2],
			preferredDatastoreChosen, ClusterdatastoreListVC[2], nil, true, multiVcClientIndex)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		pathsLen := len(preferredDatastorePaths)
		for j := 0; j < pathsLen; j++ {
			if val, ok := ClusterdatastoreListVC[2][preferredDatastorePaths[j]]; ok {
				dsUrls = append(dsUrls, val)
			}
		}

		framework.Logf("Waiting for %v for preferred datastore to get refreshed in the environment",
			preferredDatastoreTimeOutInterval)
		time.Sleep(preferredDatastoreTimeOutInterval)

		ginkgo.By("Create StorageClass and PVC")
		storageclass, pvclaim, err := createPVCAndStorageClass(client, namespace, nil,
			nil, diskSize, allowedTopologies, "", false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() {
			err := client.StorageV1().StorageClasses().Delete(ctx, storageclass.Name,
				*metav1.NewDeleteOptions(0))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		// Wait for PVC to be in Bound phase
		persistentvolumes, err := fpv.WaitForPVClaimBoundPhase(client, []*v1.PersistentVolumeClaim{pvclaim},
			framework.ClaimProvisionTimeout)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		volHandle := persistentvolumes[0].Spec.CSI.VolumeHandle
		gomega.Expect(volHandle).NotTo(gomega.BeEmpty())
		defer func() {
			err := fpv.DeletePersistentVolumeClaim(client, pvclaim.Name, namespace)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = multiVCe2eVSphere.waitForCNSVolumeToBeDeletedInMultiVC(volHandle)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			pvclaim = nil
		}()

		ginkgo.By(fmt.Sprintf("Invoking QueryCNSVolumeWithResult with VolumeID: %s", volHandle))
		queryResult, err := multiVCe2eVSphere.queryCNSVolumeWithResultInMultiVC(volHandle)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(queryResult.Volumes).ShouldNot(gomega.BeEmpty())
		gomega.Expect(queryResult.Volumes[0].VolumeId.Id).To(gomega.Equal(volHandle))

		ginkgo.By("Create volume snapshot class, volume snapshot")
		volumeSnapshot, volumeSnapshotClass, snapshotId := createSnapshotClassAndVolSnapshot(ctx, snapc, namespace,
			pvclaim, volHandle, false, true)
		defer func() {
			ginkgo.By("Perform cleanup of snapshot created")
			performCleanUpForSnapshotCreated(ctx, snapc, namespace, volHandle, volumeSnapshot, snapshotId,
				volumeSnapshotClass)
		}()

		ginkgo.By("Create PVC from snapshot")
		pvcSpec := getPersistentVolumeClaimSpecWithDatasource(namespace, diskSize, storageclass, nil,
			v1.ReadWriteOnce, volumeSnapshot.Name, snapshotapigroup)
		pvclaim2, err := fpv.CreatePVC(client, namespace, pvcSpec)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		persistentvolumes2, err := fpv.WaitForPVClaimBoundPhase(client,
			[]*v1.PersistentVolumeClaim{pvclaim2}, framework.ClaimProvisionTimeout)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		volHandle2 := persistentvolumes2[0].Spec.CSI.VolumeHandle
		gomega.Expect(volHandle2).NotTo(gomega.BeEmpty())
		defer func() {
			err := fpv.DeletePersistentVolumeClaim(client, pvclaim2.Name, namespace)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = multiVCe2eVSphere.waitForCNSVolumeToBeDeletedInMultiVC(volHandle2)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}()

		ginkgo.By("Creating pod")
		pod, err := createPod(client, namespace, nil, []*v1.PersistentVolumeClaim{pvclaim2}, false, "")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		defer func() {
			ginkgo.By(fmt.Sprintf("Deleting the pod %s in namespace %s", pod.Name, namespace))
			err = fpod.DeletePodWithWait(client, pod)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.By("Verify volume is detached from the node")
			isDiskDetached, err := multiVCe2eVSphere.waitForVolumeDetachedFromNodeInMultiVC(client,
				volHandle2, pod.Spec.NodeName)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(isDiskDetached).To(gomega.BeTrue(),
				fmt.Sprintf("Volume %q is not detached from the node %q", volHandle2,
					pod.Spec.NodeName))
		}()

		// verifying volume provisioning
		ginkgo.By("Verify volume is provisioned on the preferred datatsore")
		verifyVolumeProvisioningForStandalonePods(ctx, client, pod, namespace, preferredDatastorePaths,
			ClusterdatastoreListVC[2], true, dsUrls)

		ginkgo.By("Verify PV node affinity and that the PODS are running on " +
			"appropriate node as specified in the allowed topologies of SC")
		verifyPVnodeAffinityAndPODnodedetailsFoStandalonePodLevel5(ctx, client, pod,
			namespace, allowedTopologies, true)
	})

})

// func writeConfigToSecretString(cfg e2eTestConfig) (string, error) {
// 	result := fmt.Sprintf("[Global]\ninsecure-flag = \"%s\"\ncluster-distribution = \"%s\"\nquery-limit = %d\n"+
// 		"csi-fetch-preferred-datastores-intervalinmin = %d\nlist-volume-threshold = %d\n\n"+
// 		"[VirtualCenter \"10.161.119.92\"]\ninsecure-flag = \"%s\"\nuser = \"%s\"\npassword = \"%s\"\nport = \"%s\"\n"+
// 		"datacenters = \"%s\"\n\n"+
// 		"[VirtualCenter \"10.78.160.225\"]\ninsecure-flag = \"%s\"\nuser = \"%s\"\npassword = \"%s\"\nport = \"%s\"\n"+
// 		"datacenters = \"%s\"\n\n"+
// 		"datacenters = \"%s\"\n\n"+
// 		"[Labels]\ntopology-categories = \"%s\"",
// 		cfg.Global.InsecureFlag, cfg.Global.ClusterDistribution, cfg.Global.QueryLimit,
// 		cfg.Global.CSIFetchPreferredDatastoresIntervalInMin, cfg.Global.ListVolumeThreshold,
// 		cfg.VirtualCenter1.InsecureFlag, cfg.VirtualCenter1.User, cfg.VirtualCenter1.Password, cfg.VirtualCenter1.Port,
// 		cfg.VirtualCenter1.Datacenters,
// 		cfg.VirtualCenter2.InsecureFlag, cfg.VirtualCenter2.User, cfg.VirtualCenter2.Password, cfg.VirtualCenter2.Port,
// 		cfg.VirtualCenter2.Datacenters,
// 		cfg.VirtualCenter3.InsecureFlag, cfg.VirtualCenter3.User, cfg.VirtualCenter3.Password, cfg.VirtualCenter3.Port,
// 		cfg.VirtualCenter3.Datacenters,
// 		cfg.Snapshot.GlobalMaxSnapshotsPerBlockVolume,
// 		cfg.Labels.TopologyCategories)
// 	return result, nil
// }
