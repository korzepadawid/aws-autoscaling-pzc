package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

const (
	ENV_FILE_PATH    = ".env"
	USER_DATA_SCRIPT = "user_data.sh"

	AWS_REGION                 = "us-east-1"
	AWS_AMI_ID                 = "ami-01816d07b1128cd2d" // Amazon Linux 2023 AMI
	AWS_LAUNCH_TEMPLATE_PREFIX = "webservice-launch-template-"
	AWS_DEFAULT_EC2_COUNT      = 2
)

func main() {
	logger := log.Default()
	if err := godotenv.Load(ENV_FILE_PATH); err != nil {
		logger.Fatalf("Error loading .env file: %v", err)
	}
	logger.Println("Environment variables loaded successfully")

	ctx, cancelFunc := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancelFunc()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithDefaultRegion(AWS_REGION))
	if err != nil {
		log.Fatal(err)
	}
	logger.Println("AWS configuration loaded successfully")
	ec2Client := ec2.NewFromConfig(cfg)

	vpcID, err := CreateVPC(ctx, logger, ec2Client)
	if err != nil {
		logger.Fatal(err)
	}

	subnetID, err := CreateSubnet(ctx, logger, ec2Client, vpcID)
	if err != nil {
		logger.Fatal(err)
	}

	securityGroupID, err := CreateSecurityGroup(ctx, logger, ec2Client, vpcID)
	if err != nil {
		logger.Fatal(err)
	}

	launchTemplateID, err := CreateLaunchTemplate(ctx, logger, ec2Client, securityGroupID)
	if err != nil {
		logger.Fatal(err)
	}

	_, err = CreateEC2Instances(ctx, logger, ec2Client, launchTemplateID, subnetID)
	if err != nil {
		logger.Fatal(err)
	}
}

func CreateVPC(ctx context.Context, logger *log.Logger, ec2Client *ec2.Client) (string, error) {
	result, err := ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
	})
	if err != nil {
		return "", fmt.Errorf("error creating VPC: %w", err)
	}
	logger.Printf("VPC created with ID: %s", *result.Vpc.VpcId)

	modifyVPC := &ec2.ModifyVpcAttributeInput{
		VpcId: result.Vpc.VpcId,
		EnableDnsHostnames: &types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	}
	if _, err = ec2Client.ModifyVpcAttribute(ctx, modifyVPC); err != nil {
		return "", fmt.Errorf("error enabling DNS hostnames: %w", err)
	}
	logger.Printf("DNS hostnames enabled for VPC with ID: %s", *result.Vpc.VpcId)

	return *result.Vpc.VpcId, nil
}

func CreateSubnet(ctx context.Context, logger *log.Logger, ec2Client *ec2.Client, vpcID string) (string, error) {
	subnetResult, err := ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.0.1.0/24"),
		AvailabilityZone: aws.String(AWS_REGION),
	})
	if err != nil {
		return "", fmt.Errorf("error creating subnet: %w", err)
	}
	logger.Printf("Subnet created with ID: %s", *subnetResult.Subnet.SubnetId)

	return *subnetResult.Subnet.SubnetId, nil
}

func CreateSecurityGroup(ctx context.Context, logger *log.Logger, ec2Client *ec2.Client, vpcID string) (string, error) {
	sgName := "webservice-sg-" + uuid.NewString()
	sgDescription := "Security group for port 8080 access"

	createOutput, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(sgName),
		Description: aws.String(sgDescription),
		VpcId:       aws.String(vpcID),
	})
	if err != nil {
		return "", fmt.Errorf("error creating security group: %w", err)
	}
	logger.Printf("Created security group with ID: %s", *createOutput.GroupId)

	ec2IngressInput := &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: createOutput.GroupId,
		IpPermissions: []types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(8080),
				ToPort:     aws.Int32(8080),
				IpRanges: []types.IpRange{
					{
						CidrIp: aws.String("0.0.0.0/0"),
					},
				},
			},
		},
	}
	if _, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, ec2IngressInput); err != nil {
		return "", fmt.Errorf("error adding inbound (ingress) rule for port 8080: %w", err)
	}
	logger.Printf("Added inbound (ingress) rule for port 8080 to security group with ID: %s", *createOutput.GroupId)

	return *createOutput.GroupId, nil
}

func CreateLaunchTemplate(ctx context.Context, logger *log.Logger, ec2Client *ec2.Client, securityGroupID string) (string, error) {
	userDataBytes, err := os.ReadFile(USER_DATA_SCRIPT)
	if err != nil {
		return "", fmt.Errorf("error reading user_data.sh file: %w", err)
	}
	logger.Println("user_data.sh file read successfully")

	base64UserData := base64.StdEncoding.EncodeToString(userDataBytes)
	ec2LaunchTemplate, err := ec2Client.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			UserData:     aws.String(base64UserData),
			ImageId:      aws.String(AWS_AMI_ID),
			InstanceType: types.InstanceTypeT2Micro,
			SecurityGroupIds: []string{
				securityGroupID,
			},
		},
		LaunchTemplateName: aws.String(AWS_LAUNCH_TEMPLATE_PREFIX + uuid.NewString()),
	})
	if err != nil {
		return "", fmt.Errorf("error creating launch template: %w", err)
	}
	logger.Printf("Launch template created with ID: %s", *ec2LaunchTemplate.LaunchTemplate.LaunchTemplateId)

	return *ec2LaunchTemplate.LaunchTemplate.LaunchTemplateId, nil
}

func CreateEC2Instances(ctx context.Context, logger *log.Logger, ec2Client *ec2.Client, launchTemplateID string, subnetID string) ([]types.Instance, error) {
	input := &ec2.RunInstancesInput{
		LaunchTemplate: &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(launchTemplateID),
			Version:          aws.String("$Latest"),
		},
		MinCount: aws.Int32(AWS_DEFAULT_EC2_COUNT),
		MaxCount: aws.Int32(AWS_DEFAULT_EC2_COUNT),
		SubnetId: aws.String(subnetID),
	}
	result, err := ec2Client.RunInstances(ctx, input)
	if err != nil {
		log.Fatalf("Unable to launch instance, %v", err)
	}

	for _, instance := range result.Instances {
		logger.Printf("Launched instance with ID: %s, IP address: %s, DNS name: %s", *instance.InstanceId, *instance.PublicIpAddress, *instance.PublicDnsName)
	}

	err = WaitForInstances(ctx, ec2Client, logger, result.Instances)
	if err != nil {
		return nil, fmt.Errorf("error waiting for instances to be running: %w", err)
	}
	logger.Println("All instances are running")

	return result.Instances, nil
}

func WaitForInstances(ctx context.Context, client *ec2.Client, logger *log.Logger, instances []types.Instance) error {
	instanceIDs := make([]string, len(instances))
	for i, instance := range instances {
		instanceIDs[i] = *instance.InstanceId
	}

	input := &ec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	}

	waiter := ec2.NewInstanceRunningWaiter(client, func(irwo *ec2.InstanceRunningWaiterOptions) {
		irwo.LogWaitAttempts = true
	})

	logger.Println("Waiting for instances to be running...")
	logger.Println("This may take a few minutes...")
	return waiter.Wait(ctx, input, 5*time.Minute)
}
