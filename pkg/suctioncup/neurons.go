package suctioncup

import (
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	edgev1alpha1 "github.com/rancher/octopus/api/v1alpha1"
	api "github.com/rancher/octopus/pkg/adaptor/api/v1alpha1"
	"github.com/rancher/octopus/pkg/metrics"
	"github.com/rancher/octopus/pkg/suctioncup/connection"
	"github.com/rancher/octopus/pkg/util/object"
)

func (m *manager) ExistAdaptor(name string) bool {
	return m.adaptors.Get(name) != nil
}

func (m *manager) Connect(referencesData map[string]map[string][]byte, device *unstructured.Unstructured, by *edgev1alpha1.DeviceLink) error {
	// connection的逻辑就是在本地创建一个socket  用于和远端的grpc进行通信
	var adaptorName = by.Status.AdaptorName
	if adaptorName == "" {
		return errors.New("adaptor name is empty")
	}
	// 根据adaptor的名称获取adaptor
	var adaptor = m.adaptors.Get(adaptorName)
	if adaptor == nil {
		return errors.Errorf("cannot find adaptor %s", adaptorName)
	}

	// records metrics
	var (
		overwritten  bool
		connectedErr error
	)
	defer func() {
		if connectedErr != nil {
			metrics.GetLimbMetricsRecorder().IncreaseConnectErrors(adaptorName)
		} else if !overwritten {
			metrics.GetLimbMetricsRecorder().IncreaseConnections(adaptorName)
		}
	}()
	// 获取device 名称（也就是model的名称）
	var deviceName = object.GetNamespacedName(by)
	var conn connection.Connection
	overwritten, conn, connectedErr = adaptor.CreateConnection(deviceName)
	if connectedErr != nil {
		return errors.Wrapf(connectedErr, "cannot to link device %s via adaptor", deviceName)
	}

	// records metrics
	var (
		sendStartTS = time.Now()
		sentErr     error
	)
	defer func() {
		metrics.GetLimbMetricsRecorder().ObserveSendLatency(adaptorName, time.Since(sendStartTS))
		if sentErr != nil {
			metrics.GetLimbMetricsRecorder().IncreaseSendErrors(adaptorName)
		}
	}()

	var sendModel = by.Status.Model
	var sendDevice []byte
	sendDevice, sentErr = cleanupDevice(device).MarshalJSON()
	if sentErr != nil {
		return errors.Wrapf(sentErr, "cannot marshal device %s as JSON", deviceName)
	}
	var sendReferences map[string]*api.ConnectRequestReferenceEntry
	if len(referencesData) != 0 {
		sendReferences = make(map[string]*api.ConnectRequestReferenceEntry, len(referencesData))
		for rpName, rp := range referencesData {
			var reference = &api.ConnectRequestReferenceEntry{
				Items: make(map[string][]byte, len(rp)),
			}
			for ripName, rip := range rp {
				reference.Items[ripName] = rip
			}
			sendReferences[rpName] = reference
		}
	}
	sentErr = conn.Send(sendModel, sendDevice, sendReferences)
	if sentErr != nil {
		return errors.Wrapf(sentErr, "cannot send data to device %s via adaptor", deviceName)
	}
	return nil
}

func (m *manager) Disconnect(by *edgev1alpha1.DeviceLink) {
	var adaptorName = by.Status.AdaptorName
	if adaptorName == "" {
		return
	}
	var adaptor = m.adaptors.Get(adaptorName)
	if adaptor == nil {
		return
	}

	var exist bool
	defer func() {
		if exist {
			metrics.GetLimbMetricsRecorder().DecreaseConnections(adaptorName)
		}
	}()

	exist = adaptor.DeleteConnection(object.GetNamespacedName(by))
}

func cleanupDevice(device *unstructured.Unstructured) *unstructured.Unstructured {
	device.SetGenerateName("")
	device.SetSelfLink("")
	device.SetResourceVersion("")
	device.SetGeneration(0)
	device.SetCreationTimestamp(metav1.Time{})
	device.SetDeletionTimestamp(nil)
	device.SetDeletionGracePeriodSeconds(nil)
	device.SetFinalizers(nil)
	device.SetClusterName("")
	device.SetManagedFields(nil)
	unstructured.RemoveNestedField(device.Object, "status")
	return device
}
