/*


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

package controllers

import (
	"context"
	"fmt"
	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"

	"github.com/zzxwill/terraform-controller/api/v1beta1"
	"github.com/zzxwill/terraform-controller/controllers/util"
)

const (
	// TerraformBaseLocation is the base directory to store all Terraform JSON files
	TerraformBaseLocation = ".vela/terraform/"
	// TerraformLog is the logfile name for terraform
	TerraformLog = "terraform.log"
	// Terraform image which can run `terraform init/plan/apply`
	TerraformImage = "zzxwill/docker-terraform:0.14.9"

	workingVolumeMountPath              = "/data"
	InputTFConfigurationVolumeName      = "tf-input-configuration"
	InputTFConfigurationVolumeMountPath = "/opt/terraform"
)

const (
	TerraformConfigurationName = "main.tf.json"
	TerraformStateName         = "terraform.tfstate"
)

type ConfigMapName string

const (
	TFInputConfigMapSName ConfigMapName = "%s-tf-input"
	TFStateConfigMapName  ConfigMapName = "%s-tf-state"
)

const (
	AlicloudAcessKey  = "ALICLOUD_ACCESS_KEY"
	AlicloudSecretKey = "ALICLOUD_SECRET_KEY"
	AlicloudRegion    = "ALICLOUD_REGION"
)
const DefaultNamespace = "vela-system"

const ProviderName = "default"

const SucceededPod int32 = 1

// ConfigurationReconciler reconciles a Configuration object
type ConfigurationReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=terraform.core.oam.dev,resources=configurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=terraform.core.oam.dev,resources=configurations/status,verbs=get;update;patch

func (r *ConfigurationReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Reconciling Terraform Template...", "NamespacedName", req.NamespacedName)
	var (
		ctx               = context.Background()
		ns                = req.Namespace
		configurationName = req.Name
		// tfStateConfigMapName = fmt.Sprintf(string(TFStateConfigMapName), configurationName)
		configMap = v1.ConfigMap{}
	)

	var configuration v1beta1.Configuration
	if err := r.Get(ctx, req.NamespacedName, &configuration); err != nil {
		if kerrors.IsNotFound(err) {
			err = nil
		}
		return ctrl.Result{}, err
	}

	envs, err := prepareTFVariables(ctx, r.Client,configuration)
	if err != nil {
		return ctrl.Result{}, err
	}
	tfInputConfigMapsName := fmt.Sprintf(string(TFInputConfigMapSName), configurationName)
	job := prepareJob(configuration, envs, tfInputConfigMapsName)

	err = r.Client.Get(ctx, client.ObjectKey{Name: configurationName, Namespace: ns}, &job)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Check the existence of ConfigMap which is used to input TF configuration file
			// TODO(zzxwill) replace the configmap every time?
			if err := r.Client.Get(ctx, client.ObjectKey{Name: tfInputConfigMapsName, Namespace: ns}, &configMap); err != nil {
				if kerrors.IsNotFound(err) {
					configurationConfigMap := v1.ConfigMap{
						TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
						ObjectMeta: metav1.ObjectMeta{Name: tfInputConfigMapsName, Namespace: ns},
						Data: map[string]string{
							TerraformConfigurationName: configuration.Spec.JSON,
						},
					}
					if err := r.Client.Create(ctx, &configurationConfigMap); err != nil {
						return ctrl.Result{}, err
					}
				} else {
					return ctrl.Result{}, err
				}
			}

			if err := r.Client.Create(ctx, &job); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if job.Status.Succeeded == SucceededPod {
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func prepareJob(configuration v1beta1.Configuration, envs []v1.EnvVar, tfInputConfigMapsName string) batchv1.Job {
	configurationName := configuration.Name
	workingVolume := v1.Volume{Name: configurationName}
	workingVolume.EmptyDir = &v1.EmptyDirVolumeSource{}

	configMapVolumeSource := v1.ConfigMapVolumeSource{}
	configMapVolumeSource.Name = tfInputConfigMapsName
	inputTFConfigurationVolume := v1.Volume{Name: InputTFConfigurationVolumeName}
	inputTFConfigurationVolume.ConfigMap = &configMapVolumeSource

	var parallelism int32 = 1
	var completions int32 = 1
	return batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configurationName,
			Namespace: configuration.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: configuration.APIVersion,
				Kind:       configuration.Kind,
				Name:       configurationName,
				UID:        configuration.UID,
				Controller: pointer.BoolPtr(false),
			}},
		},
		Spec: batchv1.JobSpec{
			Parallelism: &parallelism,
			Completions: &completions,
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					//InitContainers: []v1.Container{{
					//	Name:    "prepare-input-terraform-configurations",
					//	Image:   "busybox",
					//	Command: []string{"sh", "-c", fmt.Sprintf("cp -r %s/* %s", InputTFConfigurationVolumeMountPath, workingVolumeMountPath)},
					//	VolumeMounts: []v1.VolumeMount{
					//		{
					//			Name:      configurationName,
					//			MountPath: workingVolumeMountPath,
					//		},
					//		{
					//			Name:      InputTFConfigurationVolumeName,
					//			MountPath: InputTFConfigurationVolumeMountPath,
					//		},
					//	},
					//}},
					Containers: []v1.Container{{
						Name:            configurationName,
						Image:           TerraformImage,
						ImagePullPolicy: v1.PullAlways,
						Command: []string{
							"bash",
							"-c",
							fmt.Sprintf("cp %s/* %s && terraform init && terraform apply -auto-approve", InputTFConfigurationVolumeMountPath, workingVolumeMountPath),
						},
						VolumeMounts: []v1.VolumeMount{
							{
								Name:      InputTFConfigurationVolumeName,
								MountPath: InputTFConfigurationVolumeMountPath,
							},
						},
						Env: envs,
					}},
					Volumes:       []v1.Volume{workingVolume, inputTFConfigurationVolume},
					RestartPolicy: v1.RestartPolicyNever,
				},
			},
		},
	}

}

func prepareTFVariables(ctx context.Context, k8sClient client.Client, configuration v1beta1.Configuration) ([]v1.EnvVar, error) {
	var envs []v1.EnvVar

	tfVariable, err := getTerraformJSONVariable(configuration)
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to get Terraform JSON variables from Configuration %s", configuration.Name))
	}

	for k, v := range tfVariable {
		envs = append(envs, v1.EnvVar{Name: k, Value: v})
	}

	ak, err := getProviderSecret(ctx, k8sClient)
	if err != nil {
		return nil, err
	}
	envs = append(envs,
		v1.EnvVar{
			Name:  AlicloudAcessKey,
			Value: ak.AccessKeyId,
		},
		v1.EnvVar{
			Name:  AlicloudSecretKey,
			Value: ak.AccessKeySecret,
		},
		v1.EnvVar{
			Name:  AlicloudRegion,
			Value: ak.Region,
		},
	)
	return envs, nil
}

type Variable map[string]interface{}

func (r *ConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Configuration{}).
		Complete(r)
}

func getTerraformJSONVariable(c v1beta1.Configuration) (map[string]string, error) {
	variables, err := util.RawExtension2Map(&c.Spec.Variable)
	if err != nil {
		return nil, err
	}
	var environments = make(map[string]string)

	for k, v := range variables {
		environments[fmt.Sprintf("TF_VAR_%s", k)] = fmt.Sprint(v)
	}
	return environments, nil
}

// generateSecretFromTerraformOutput generates secret from Terraform output
func generateSecretFromTerraformOutput(k8sClient client.Client, outputList []string, name, namespace string) error {
	ctx := context.TODO()
	err := k8sClient.Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})
	if err == nil {
		return fmt.Errorf("namespace %s doesn't exist", namespace)
	}
	var cmData = make(map[string]string, len(outputList))
	for _, i := range outputList {
		line := strings.Split(i, "=")
		if len(line) != 2 {
			return fmt.Errorf("terraform output isn't in the right format")
		}
		k := strings.TrimSpace(line[0])
		v := strings.TrimSpace(line[1])
		if k != "" && v != "" {
			cmData[k] = v
		}
	}

	objectKey := client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}
	var secret v1.Secret
	if err := k8sClient.Get(ctx, objectKey, &secret); err != nil && !kerrors.IsNotFound(err) {
		return fmt.Errorf("retrieving the secret from cloud resource %s hit an issue: %w", name, err)
	} else if err == nil {
		if err := k8sClient.Delete(ctx, &secret); err != nil {
			return fmt.Errorf("failed to store cloud resource %s output to secret: %w", name, err)
		}
	}

	secret = v1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		StringData: cmData,
	}

	if err := k8sClient.Create(ctx, &secret); err != nil {
		return fmt.Errorf("failed to store cloud resource %s output to secret: %w", name, err)
	}
	return nil
}

func getProviderSecret(ctx context.Context, k8sClient client.Client) (*util.AlibabaCloudCredentials, error) {
	var provider v1beta1.Provider
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: ProviderName, Namespace: "default"}, &provider); err != nil {
		errMsg := "failed to get Provider object"
		klog.ErrorS(err, errMsg, "Name", ProviderName)
		return nil, errors.Wrap(err, errMsg)
	}

	switch provider.Spec.Credentials.Source {
	case "Secret":
		var secret v1.Secret
		secretRef := provider.Spec.Credentials.SecretRef
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: secretRef.Name, Namespace: secretRef.Namespace}, &secret); err != nil {
			errMsg := "failed to get the Secret from Provider"
			klog.ErrorS(err, errMsg, "Name", secretRef.Name, "Namespace", secretRef.Namespace)
			return nil, errors.Wrap(err, errMsg)
		}
		var ak util.AlibabaCloudCredentials
		if err := yaml.Unmarshal(secret.Data[secretRef.Key], &ak); err != nil {
			errMsg := "failed to convert the credentials of Secret from Provider"
			klog.ErrorS(err, errMsg, "Name", secretRef.Name, "Namespace", secretRef.Namespace)
			return nil, errors.Wrap(err, errMsg)
		}
		ak.Region = provider.Spec.Region
		return &ak, nil
	default:
		errMsg := "the credentials type is not supported."
		err := errors.New(errMsg)
		klog.ErrorS(err, "", "CredentialType", provider.Spec.Credentials.Source)
		return nil, err
	}
}
