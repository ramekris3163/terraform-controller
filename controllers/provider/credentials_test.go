package provider

import (
	"context"
	"reflect"
	"testing"

	types "github.com/oam-dev/terraform-controller/api/types/crossplane-runtime"
	"github.com/oam-dev/terraform-controller/api/v1beta1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCheckAlibabaCloudCredentials(t *testing.T) {
	type credentials struct {
		AccessKeyID     string
		AccessKeySecret string
		SecurityToken   string
		Region          string
	}

	type args struct {
		credentials credentials
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "Check AlibabaCloud credentials",
			args: args{
				credentials: credentials{
					AccessKeyID:     "aaaa",
					AccessKeySecret: "bbbbb",
					Region:          "cn-hangzhou",
				},
			},
		},
		{
			name: "Check AlibabaCloud credentials with sts token",
			args: args{
				credentials: credentials{
					AccessKeyID:     "STS.aaaa",
					AccessKeySecret: "bbbbb",
					SecurityToken:   "ccc",
					Region:          "cn-hangzhou",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred := tt.args.credentials
			err := checkAlibabaCloudCredentials(cred.Region, cred.AccessKeyID, cred.AccessKeySecret, cred.SecurityToken)
			assert.NotNil(t, err)
		})
	}

}

func newFakeClient() client.Client {
	objects := []runtime.Object{
		&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-custom",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"credentials": []byte("Token: mytoken"),
			},
			Type: v1.SecretTypeOpaque,
		},
	}

	return fake.NewFakeClient(objects...)
}

func TestGetProviderCredentials(t *testing.T) {
	ctx := context.TODO()
	client := newFakeClient()

	type args struct {
		provider *v1beta1.Provider
		region   string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]string
		wantErr bool
	}{
		// TODO: Add test cases.
		// basic
		{
			name: "Other source",
			args: args{
				provider: &v1beta1.Provider{
					Spec: v1beta1.ProviderSpec{
						Credentials: v1beta1.ProviderCredentials{
							Source: "Nil",
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "Secret not found",
			args: args{
				provider: &v1beta1.Provider{
					Spec: v1beta1.ProviderSpec{
						Credentials: v1beta1.ProviderCredentials{
							Source: "Secret",
							SecretRef: &types.SecretKeySelector{
								SecretReference: types.SecretReference{
									Name:      "nil",
									Namespace: "default",
								},
								Key: "credentials",
							},
						},
					},
				},
			},
			wantErr: true,
		},

		// Provider
		{
			name: "Custom Provider",
			args: args{
				provider: &v1beta1.Provider{
					Spec: v1beta1.ProviderSpec{
						Provider: string(custom),
						Credentials: v1beta1.ProviderCredentials{
							Source: "Secret",
							SecretRef: &types.SecretKeySelector{
								SecretReference: types.SecretReference{
									Name:      "test-custom",
									Namespace: "default",
								},
								Key: "credentials",
							},
						},
					},
				},
			},
			want: map[string]string{
				"Token": "mytoken",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetProviderCredentials(ctx, client, tt.args.provider, tt.args.region)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetProviderCredentials() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetProviderCredentials() = %v, want %v", got, tt.want)
			}
		})
	}
}