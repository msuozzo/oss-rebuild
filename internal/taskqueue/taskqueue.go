package taskqueue

import (
	"context"
	"fmt"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"

	"github.com/pkg/errors"
)

type Queue interface {
	Add(ctx context.Context, url, body string) (*taskspb.Task, error)
}

type queue struct {
	client              *cloudtasks.Client
	queuePath           string
	serviceAccountEmail string
}

func NewQueue(ctx context.Context, queuePath, serviceAccountEmail string) (Queue, error) {
	client, err := cloudtasks.NewClient(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "creating TaskQueue client")
	}
	return &queue{
		client:              client,
		queuePath:           queuePath,
		serviceAccountEmail: serviceAccountEmail,
	}, nil
}

func (q *queue) Add(ctx context.Context, url, body string) (*taskspb.Task, error) {
	req := &taskspb.CreateTaskRequest{
		Parent: q.queuePath,
		Task: &taskspb.Task{
			MessageType: &taskspb.Task_HttpRequest{
				HttpRequest: &taskspb.HttpRequest{
					HttpMethod: taskspb.HttpMethod_POST,
					Url:        url,
					Headers: map[string]string{
						"Content-Type": "application/x-www-form-urlencoded",
					},
					Body: []byte(body),
					AuthorizationHeader: &taskspb.HttpRequest_OidcToken{
						OidcToken: &taskspb.OidcToken{
							ServiceAccountEmail: q.serviceAccountEmail,
						},
					},
				},
			},
		},
	}
	task, err := q.client.CreateTask(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("cloudtasks.CreateTask: %w", err)
	}
	return task, nil
}
