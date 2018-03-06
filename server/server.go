package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/davecgh/go-spew/spew"
	"github.com/golang/protobuf/ptypes"
	"github.com/grandcat/zeroconf"
	"github.com/pkg/errors"
	"github.com/scottjab/multiboard/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const tokenHeader = "x-multiboardproto-token"

type Mutliboard struct {
	Host, Password string

	Broadcast chan multiboardProto.StreamResponse

	ClientNames   map[string]string
	ClientStreams map[string]chan multiboardProto.StreamResponse

	namesMtx, streamsMtx sync.RWMutex
}

func Server(host, pass string) *Mutliboard {
	return &Mutliboard{
		Host:      host,
		Password:  pass,
		Broadcast: make(chan multiboardProto.StreamResponse, 1000),

		ClientNames:   make(map[string]string),
		ClientStreams: make(map[string]chan multiboardProto.StreamResponse),
	}
}

func (pc *Mutliboard) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log.Printf("starting on %s with password %q", pc.Host, pc.Password)
	srv := grpc.NewServer()
	multiboardProto.RegisterMultiBoardServer(srv, pc)
	l, err := net.Listen("tcp", pc.Host)
	if err != nil {
		return errors.WithMessage(err, "server unable to bind to provided host")
	}

	go pc.broadcast(ctx)
	go func() {
		srv.Serve(l)
		cancel()
	}()

	<-ctx.Done()
	pc.Broadcast <- multiboardProto.StreamResponse{
		Timestamp: ptypes.TimestampNow(),
		Event: &multiboardProto.StreamResponse_ServerShutdown{
			&multiboardProto.StreamResponse_Shutdown{}}}
	close(pc.Broadcast)
	srv.GracefulStop()
	return nil

}
func (pc *Mutliboard) broadcast(ctx context.Context) {
	for res := range pc.Broadcast {
		pc.streamsMtx.Lock()
		for _, stream := range pc.ClientStreams {
			select {
			case stream <- res:
				//nooop
			default:
				log.Printf("client stream full, dropping message")
			}
		}
		pc.streamsMtx.Unlock()
	}
}

func (pc *Mutliboard) Login(ctx context.Context, req *multiboardProto.LoginRequest) (*multiboardProto.LoginResponse, error) {

	switch {
	case req.Password != pc.Password:
		return nil, status.Error(codes.Unauthenticated, "password is incorrect.")
	case req.Name == "":
		return nil, status.Error(codes.InvalidArgument, "username is required")
	}

	tkn := pc.genToken()
	pc.setName(tkn, req.Name)

	log.Printf("%s (%s) has logged in", tkn, req.Name)
	pc.Broadcast <- multiboardProto.StreamResponse{
		Timestamp: ptypes.TimestampNow(),
		Event: &multiboardProto.StreamResponse_ClientLogin{
			&multiboardProto.StreamResponse_Login{
				Name: req.Name,
			},
		},
	}
	return &multiboardProto.LoginResponse{Token: tkn}, nil
}

func (pc *Mutliboard) Logout(ctx context.Context, req *multiboardProto.LogoutRequest) (*multiboardProto.LogoutResponse, error) {
	name, ok := pc.delName(req.Token)
	if !ok {
		return nil, status.Error(codes.NotFound, "token not found")
	}
	log.Printf("%s (%s) has logged out", req.Token, name)

	pc.Broadcast <- multiboardProto.StreamResponse{
		Timestamp: ptypes.TimestampNow(),
		Event: &multiboardProto.StreamResponse_ClientLogout{
			&multiboardProto.StreamResponse_Logout{
				Name: name,
			},
		},
	}
	return new(multiboardProto.LogoutResponse), nil
}

func (pc *Mutliboard) Stream(srv multiboardProto.MultiBoard_StreamServer) error {
	spew.Println(srv.Context())
	tkn, ok := pc.extractToken(srv.Context())
	if !ok {
		log.Println("missing token header")
		return status.Error(codes.Unauthenticated, "missing token header")
	}
	name, ok := pc.getName(tkn)
	if !ok {
		log.Println("Invalid Token")
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	go pc.sendBroadcasts(srv, tkn)
	for {
		req, err := srv.Recv()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		pc.Broadcast <- multiboardProto.StreamResponse{
			Timestamp: ptypes.TimestampNow(),
			Event: &multiboardProto.StreamResponse_ClientMessage{
				&multiboardProto.StreamResponse_Message{
					Name:  name,
					Pixel: req.GetServerDraw().Pixel,
				}},
		}

	}
	<-srv.Context().Done()
	return srv.Context().Err()
}

func (pc *Mutliboard) sendBroadcasts(srv multiboardProto.MultiBoard_StreamServer, tkn string) {
	stream := pc.openStream(tkn)
	defer pc.closeStream(tkn)
	for {
		select {
		case <-srv.Context().Done():
			return
		case res := <-stream:
			if s, ok := status.FromError(srv.Send(&res)); ok {
				switch s.Code() {
				case codes.OK:
					// noop
				case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded:
					log.Printf("client (%s) terminated connection", tkn)
					return
				default:
					log.Printf("failed to send to client (%s): %v", tkn, s.Err())
					return
				}
			}
		}
	}

}
func (pc *Mutliboard) openStream(tkn string) chan multiboardProto.StreamResponse {
	stream := make(chan multiboardProto.StreamResponse, 100)

	pc.streamsMtx.Lock()

	pc.ClientStreams[tkn] = stream
	pc.streamsMtx.Unlock()
	log.Printf("opened stream for client %s", tkn)

	return stream
}

func (pc *Mutliboard) closeStream(tkn string) {
	pc.streamsMtx.Lock()

	if stream, ok := pc.ClientStreams[tkn]; ok {
		delete(pc.ClientStreams, tkn)
		close(stream)
	}
	pc.streamsMtx.Unlock()
	log.Printf("closed stream for client %s", tkn)
}

func (pc *Mutliboard) genToken() string {
	//TODO: fix me
	tkn := make([]byte, 4)
	rand.Read(tkn)
	return fmt.Sprintf("%x", tkn)
}

func (pc *Mutliboard) getName(tkn string) (string, bool) {
	pc.namesMtx.RLock()
	name, ok := pc.ClientNames[tkn]
	pc.namesMtx.RUnlock()
	return name, ok
}

func (pc *Mutliboard) setName(tkn string, name string) {
	pc.namesMtx.Lock()
	pc.ClientNames[tkn] = name
	pc.namesMtx.Unlock()
}

func (pc *Mutliboard) delName(tkn string) (string, bool) {
	name, ok := pc.getName(tkn)
	if ok {
		pc.namesMtx.Lock()
		defer pc.namesMtx.Unlock()
		delete(pc.ClientNames, tkn)

	}
	return name, ok
}

func (pc *Mutliboard) extractToken(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md[tokenHeader]) == 0 {
		return "", false
	}
	return md[tokenHeader][0], true
}

func SignalContext(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Println("listening for shutdown signal")
		<-sigs
		log.Println("shutdown signal received")
		signal.Stop(sigs)
		close(sigs)
		cancel()
	}()

	return ctx
}

func main() {
	ctx := SignalContext(context.Background())

	server, err := zeroconf.Register(
		"MultiBoard",
		"_workstation._tcp",
		"local.",
		6669,
		[]string{"txtv=0", "lo=1", "la=2"},
		nil,
	)
	if err != nil {
		log.Fatalln("zeroconf failed", err)
	}
	defer server.Shutdown()

	err = Server(":6669", "thisisapassword").Run(ctx)
	if err != nil {
		log.Fatalln("Something Happened ", err)
	}
}
