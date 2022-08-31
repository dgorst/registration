package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"net/url"
	"strings"
)

const (
	v1Prefix             = "k8s-aws-v1."
	eksClusterHeader     = "x-k8s-aws-id"
	eksTokenExpiryHeader = "X-Amz-Expires"
)

// BuildHubClient returns an error when accept has not been run on the hub cluster yet
func (c client) BuildHubClient(ctx context.Context, sessionName string) (*rest.Config, error) {
	// TODO(@dgorst) - handle token expiry gracefully <60 minutes

	credentialProvider := assumedCredentialProvider{
		roleArn:     c.hubRole,
		stsClient:   c.stsClient,
		sessionName: sessionName,
	}

	hubAccountStsClient := sts.NewFromConfig(aws.Config{
		Region:             c.hubClusterRegion,
		Credentials:        credentialProvider,
		RuntimeEnvironment: aws.RuntimeEnvironment{},
	})

	hubAccountEksClient := eks.NewFromConfig(aws.Config{
		Region:             c.hubClusterRegion,
		Credentials:        credentialProvider,
		RuntimeEnvironment: aws.RuntimeEnvironment{},
	})

	// See https://github.com/kubernetes-sigs/aws-iam-authenticator#api-authorization-from-outside-a-cluster
	preSignClient := sts.NewPresignClient(hubAccountStsClient)
	preSignedUrlRequest, err := preSignClient.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(presignOptions *sts.PresignOptions) {
		presignOptions.ClientOptions = append(presignOptions.ClientOptions, func(stsOptions *sts.Options) {
			stsOptions.APIOptions = append(stsOptions.APIOptions, smithyhttp.SetHeaderValue(eksClusterHeader, c.hubEksClusterName))
			stsOptions.APIOptions = append(stsOptions.APIOptions, smithyhttp.SetHeaderValue(eksTokenExpiryHeader, "600"))
		})
	})
	if err != nil {
		klog.Errorf("unable to get signed request to generate auth token - %s", err)
		return nil, err
	}
	hubK8sToken := v1Prefix + base64.RawURLEncoding.EncodeToString([]byte(preSignedUrlRequest.URL))

	hubEks, err := hubAccountEksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(c.hubEksClusterName),
	})
	if err != nil {
		klog.Errorf("unable to describe hub cluster %s - %s", c.hubEksClusterName, err)
		return nil, err
	}

	certByt, err := base64.StdEncoding.DecodeString(aws.ToString(hubEks.Cluster.CertificateAuthority.Data))
	if err != nil {
		klog.Errorf("invalid ca data received from eks describe cluster", err)
		return nil, err
	}

	u, err := url.Parse(aws.ToString(hubEks.Cluster.Endpoint))
	if err != nil {
		klog.Errorf("invalid cluster endpoint received from eks describe cluster", err)
		return nil, err
	}

	return &rest.Config{
		Host:        strings.ToLower(u.Host),
		BearerToken: hubK8sToken,
		TLSClientConfig: rest.TLSClientConfig{
			CAData:     certByt,
			ServerName: strings.ToLower(u.Host),
		},
	}, nil
}

func CreateKubeconfig(cfg *rest.Config) clientcmdapi.Config {
	return clientcmdapi.Config{
		Kind:        "Config",
		APIVersion:  "v1",
		Preferences: clientcmdapi.Preferences{},
		Clusters: map[string]*clientcmdapi.Cluster{
			"default-cluster": {
				Server:                   fmt.Sprintf("https://%s", cfg.Host),
				InsecureSkipTLSVerify:    false,
				CertificateAuthorityData: cfg.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default-auth": {
				Token: cfg.BearerToken,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default-context": {
				Cluster:  "default-cluster",
				AuthInfo: "default-auth",
			},
		},
		CurrentContext: "default-context",
	}
}

type assumedCredentialProvider struct {
	sessionName string
	roleArn     string
	stsClient   *sts.Client
}

func (p assumedCredentialProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	// TODO(@dgorst) - should be AssumeRoleWithWebIdentity?
	assumedCredentials, err := p.stsClient.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String(p.roleArn),
		RoleSessionName: aws.String(p.sessionName),
	})
	if err != nil {
		return aws.Credentials{}, err
	}
	return aws.Credentials{
		AccessKeyID:     aws.ToString(assumedCredentials.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(assumedCredentials.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(assumedCredentials.Credentials.SessionToken),
		Expires:         aws.ToTime(assumedCredentials.Credentials.Expiration),
	}, nil
}
