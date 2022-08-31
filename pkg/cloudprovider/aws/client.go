package aws

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"os"
)

type client struct {
	hubRole           string
	hubEksClusterName string
	hubClusterRegion  string
	stsClient         *sts.Client
}

type Opts struct {
	HubRoleArn        string
	HubEksClusterName string
	HubRegion         string
	WorkerRegion      string
}

func NewFromDefaultConfig(opts Opts) (*client, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if opts.WorkerRegion == "" {
		opts.WorkerRegion = os.Getenv("AWS_REGION")
	}
	cfg.Region = opts.WorkerRegion
	if err != nil {
		return nil, err
	}
	return &client{
		hubRole:           opts.HubRoleArn,
		hubEksClusterName: opts.HubEksClusterName,
		hubClusterRegion:  opts.HubRegion,
		stsClient: sts.NewFromConfig(cfg, func(o *sts.Options) {
			o.Retryer = aws.NopRetryer{}
		}),
	}, nil
}
