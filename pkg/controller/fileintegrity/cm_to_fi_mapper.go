package fileintegrity

import (
	"context"
	fileintegrityv1alpha1 "github.com/openshift/file-integrity-operator/pkg/apis/fileintegrity/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type fileIntegrityMapper struct {
	client.Client
}

func (s *fileIntegrityMapper) Map(obj handler.MapObject) []reconcile.Request {
	var requests []reconcile.Request

	fiList := fileintegrityv1alpha1.FileIntegrityList{}
	err := s.List(context.TODO(), &fiList, &client.ListOptions{})
	if err != nil {
		return requests
	}

	for _, fi := range fiList.Items {
		if fi.Spec.Config.Name != obj.Meta.GetName() {
			continue
		}

		if fi.Spec.Config.Namespace != obj.Meta.GetNamespace() {
			continue
		}

		objKey := types.NamespacedName{
			Name:      fi.GetName(),
			Namespace: fi.GetNamespace(),
		}
		requests = append(requests, reconcile.Request{NamespacedName: objKey})
		log.Info("Will reconcile FI because its config changed",
			"configMap.Name", obj.Meta.GetName(), "configMap.Namespace",
			obj.Meta.GetNamespace(), "fileIntegrity.Name", fi.Name, "fileIntegrity.Namespace", fi.Namespace)
	}

	return requests
}
