package vaif_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	vaif "github.com/vaifstudio/vaif-go"
)

func Example() {
	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...",
		vaif.WithTimeout(15*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	res, err := client.Auth.SignIn(ctx, "ada@example.com", "a strong password")
	if err != nil {
		log.Fatal(err)
	}
	if res.MFARequired {
		if _, err := client.Auth.VerifyMFA(ctx, res.ChallengeToken, "123456"); err != nil {
			log.Fatal(err)
		}
	}

	todos, err := client.From("todos").Eq("done", false).Order("created_at", true).Limit(20).List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(todos.Count, len(todos.Data))
}

func ExampleQuery_ListInto() {
	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...")
	if err != nil {
		log.Fatal(err)
	}
	type Todo struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Done  bool   `json:"done"`
	}
	var todos []Todo
	total, err := client.From("todos").ILike("title", "%milk%").ListInto(context.Background(), &todos)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(total, todos)
}

func ExampleError() {
	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...")
	if err != nil {
		log.Fatal(err)
	}
	_, err = client.From("todos").Get(context.Background(), "missing-id")
	var apiErr *vaif.Error
	switch {
	case err == nil:
		fmt.Println("found")
	case errors.Is(err, vaif.ErrNotFound):
		fmt.Println("no such row")
	case errors.As(err, &apiErr):
		fmt.Println(apiErr.Status, apiErr.Code, apiErr.RequestID)
	}
}

func ExampleStorageClient_Upload() {
	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.Storage.Upload(ctx, "avatars/u1.png", []byte("..."), &vaif.UploadOptions{ContentType: "image/png"}); err != nil {
		log.Fatal(err)
	}
	url, err := client.Storage.CreateSignedURL(ctx, "avatars/u1.png", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(url)
}

func ExampleFunctionsClient_Invoke() {
	client, err := vaif.NewClient("YOUR_PROJECT_ID", "vk_pub_...")
	if err != nil {
		log.Fatal(err)
	}
	resp, err := client.Functions.Invoke(context.Background(), "send-email", map[string]any{"to": "ada@example.com"}, nil)
	if err != nil {
		log.Fatal(err)
	}
	var out struct {
		Sent bool `json:"sent"`
	}
	if err := resp.JSON(&out); err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Status, out.Sent)
}
