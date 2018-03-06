package client

import (
	"log"
	"time"

	"io"
	"os"
	"syscall"

	"github.com/pkg/errors"
	"github.com/scottjab/multiboard/proto"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const tokenHeader = "x-multiboardproto-token"

type Client struct {
	multiboardProto.MultiBoardClient
	Host, Password, Name, Token string
	Shutdown                    bool

	SendChan chan multiboardProto.StreamRequest
	RecvChan chan<- multiboardProto.StreamResponse_Message
}

func (c *Client) Run(ctx context.Context) error {
	connCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	conn, err := grpc.DialContext(connCtx, c.Host, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		return errors.WithMessage(err, "unable to connect")
	}
	defer conn.Close()

	c.MultiBoardClient = multiboardProto.NewMultiBoardClient(conn)
	if c.Token, err = c.login(ctx); err != nil {
		return errors.WithMessage(err, "failed to login")
	}
	log.Println("Logged in successfully!")

	md := metadata.New(map[string]string{tokenHeader: c.Token})
	ctx = metadata.NewOutgoingContext(ctx, md)
	nctx, cancel := context.WithCancel(ctx)
	err = c.stream(nctx)
	cancel()
	log.Println("logged out")
	if err := c.logout(nctx); err != nil {
		log.Println("failed to logout", err)
	}
	return errors.WithMessage(err, "stream error")
}

func (c *Client) login(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	res, err := c.MultiBoardClient.Login(ctx, &multiboardProto.LoginRequest{
		Name:     c.Name,
		Password: c.Password,
	})

	if err != nil {
		return "", err
	}
	log.Println("token", res.Token)
	return res.Token, nil
}
func (c *Client) logout(ctx context.Context) error {
	if c.Shutdown {
		log.Println("undable to logout (server sent shutdown signal)")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := c.MultiBoardClient.Logout(ctx, &multiboardProto.LogoutRequest{Token: c.Token})
	if s, ok := status.FromError(err); ok && s.Code() == codes.Unavailable {
		log.Println("unable to logout (connection already closed)")
		return nil
	}

	return err
}

func (c *Client) stream(ctx context.Context) error {
	client, err := c.MultiBoardClient.Stream(ctx)
	if err != nil {
		return err
	}
	defer client.CloseSend()
	log.Println("Connected to stream")
	go c.send(client)
	return c.receive(client)
}

func (c *Client) receive(sc multiboardProto.MultiBoard_StreamClient) error {
	for {
		res, err := sc.Recv()
		if s, ok := status.FromError(err); ok && s.Code() == codes.Canceled {
			log.Println("stream canceled")
		} else if err == io.EOF {
			log.Println("stream closed by server")
		} else if err != nil {
			return err
		}
		switch evt := res.Event.(type) {
		case *multiboardProto.StreamResponse_ClientLogin:
			log.Printf("%s has logged in", evt.ClientLogin.Name)
		case *multiboardProto.StreamResponse_ClientLogout:
			log.Printf("%s has logged out", evt.ClientLogout.Name)
		case *multiboardProto.StreamResponse_ClientMessage:
			if evt.ClientMessage.Name != c.Name {
				c.RecvChan <- *evt.ClientMessage
			}
		case *multiboardProto.StreamResponse_ServerShutdown:
			log.Printf("the server is shutting down")
			c.Shutdown = true
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		default:
			log.Printf("unexpected event from the server: %T", evt)
			return nil
		}
	}
}

func (c *Client) send(client multiboardProto.MultiBoard_StreamClient) {
	for {
		select {
		case <-client.Context().Done():
			log.Println("Client send loop disconnected")
		case msg := <-c.SendChan:
			log.Println("sending mesg")
			if err := client.Send(&msg); err != nil {
				log.Println("failed to send message: %v", err)
			}
		}
	}
}

func NewClient(host, pass, name string, send chan multiboardProto.StreamRequest, recv chan<- multiboardProto.StreamResponse_Message) *Client {
	return &Client{
		Host:     host,
		Password: pass,
		Name:     name,
		SendChan: send,
		RecvChan: recv,
	}
}
