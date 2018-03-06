package multiboard

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/hajimehoshi/ebiten"
	"github.com/hajimehoshi/ebiten/ebitenutil"
	"github.com/scottjab/multiboard/client"
	"github.com/scottjab/multiboard/proto"
	"golang.org/x/net/context"
)

const (
	ScreenWidth  = 414
	ScreenHeight = 736
)

type MultiBoard struct {
	count       int
	brushImage  *ebiten.Image
	canvasImage *ebiten.Image
	StateLock   sync.RWMutex
	name        string
	C           *client.Client
	SendChan    chan multiboardProto.StreamRequest
	RecvChan    chan multiboardProto.StreamResponse_Message
	State       map[string]NodeState
}

type NodeState struct {
	lastPixel *multiboardProto.Pixel
	pixels    []multiboardProto.Pixel
}

func NewMultiBoard(host string) *MultiBoard {
	rand.Seed(time.Now().UnixNano())

	const (
		a0 = 0x00
		a1 = 0xc0
		a2 = 0xff
	)
	pixels := []uint8{
		a0, a0, a0, a1, a1, a0, a0, a0,
		a0, a0, a1, a1, a1, a1, a0, a0,
		a0, a1, a2, a2, a2, a2, a1, a0,
		a1, a1, a2, a2, a2, a2, a1, a1,
		a1, a1, a2, a2, a2, a2, a1, a1,
		a0, a1, a2, a2, a2, a2, a1, a0,
		a0, a0, a1, a1, a1, a1, a0, a0,
		a0, a0, a0, a1, a1, a0, a0, a0,
	}
	brushImage, _ := ebiten.NewImageFromImage(&image.Alpha{
		Pix:    pixels,
		Stride: 8,
		Rect:   image.Rect(0, 0, 8, 8),
	}, ebiten.FilterDefault)
	canvasImage, _ := ebiten.NewImage(ScreenWidth, ScreenHeight, ebiten.FilterNearest)
	canvasImage.Fill(color.Black)
	SendChan := make(chan multiboardProto.StreamRequest)
	RecvChan := make(chan multiboardProto.StreamResponse_Message)
	name := fmt.Sprintf("client-%d", rand.Int())
	return &MultiBoard{
		count:       0,
		brushImage:  brushImage,
		canvasImage: canvasImage,
		State:       map[string]NodeState{},
		name:        name,
		SendChan:    SendChan,
		RecvChan:    RecvChan,
		C:           client.NewClient(host, "thisisapassword", name, SendChan, RecvChan),
	}
}

func (mb *MultiBoard) paint(canvas *ebiten.Image, pixel multiboardProto.Pixel) {
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(pixel.X), float64(pixel.Y))
	op.ColorM.Scale(float64(pixel.ColorR), float64(pixel.ColorG), float64(pixel.ColorB), float64(pixel.Alpha))
	canvas.DrawImage(mb.brushImage, op)
}

func (mb *MultiBoard) Update(screen *ebiten.Image) error {
	drawn := false
	// Paint the brush by mouse dragging

	mx, my := ebiten.CursorPosition()
	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		proto := multiboardProto.Pixel{
			X:      float32(mx),
			Y:      float32(my),
			ColorR: 1.0,
			ColorG: 0.0,
			ColorB: 0.0,
			Alpha:  1.0,
		}
		mb.paint(mb.canvasImage, proto)
		mb.SendChan <- multiboardProto.StreamRequest{
			Timestamp: ptypes.TimestampNow(),
			Event: &multiboardProto.StreamRequest_ServerDraw{
				&multiboardProto.StreamRequest_Draw{
					&proto,
				},
			},
		}
		drawn = true
	}
	// Paint the brush by touches
	for _, t := range ebiten.Touches() {
		x, y := t.Position()
		proto := multiboardProto.Pixel{
			X:      float32(x),
			Y:      float32(y),
			ColorR: 1.0,
			ColorG: 0.0,
			ColorB: 0.0,
			Alpha:  1.0,
		}
		mb.paint(mb.canvasImage, proto)
		mb.SendChan <- multiboardProto.StreamRequest{
			Timestamp: ptypes.TimestampNow(),
			Event: &multiboardProto.StreamRequest_ServerDraw{
				&multiboardProto.StreamRequest_Draw{
					&proto,
				},
			},
		}

		drawn = true
	}
	if drawn {
		mb.count++
	}
	if ebiten.IsRunningSlowly() {
		return nil
	}

	mb.StateLock.Lock()
	for key, nodeState := range mb.State {
		var lp *multiboardProto.Pixel
		for _, pixel := range nodeState.pixels {
			if nodeState.lastPixel == nil {
				mb.paint(mb.canvasImage, pixel)
			}
			mb.paint(mb.canvasImage, pixel)
			nodeState.lastPixel = &pixel
			lp = nodeState.lastPixel
		}
		mb.State[key] = NodeState{lastPixel: lp}
	}
	mb.StateLock.Unlock()
	screen.DrawImage(mb.canvasImage, nil)

	msg := fmt.Sprintf("(%d, %d)", mx, my)
	for _, t := range ebiten.Touches() {
		x, y := t.Position()
		msg += fmt.Sprintf("\n(%d, %d) touch %d", x, y, t.ID())
	}
	ebitenutil.DebugPrint(screen, msg)
	return nil

}

func (mb *MultiBoard) StateUpatater() {
	for {
		select {
		case s := <-mb.RecvChan:
			mb.StateLock.Lock()
			st := mb.State[s.Name]
			st.pixels = append(st.pixels, *s.Pixel)
			mb.State[s.Name] = st
			mb.StateLock.Unlock()
		}
	}
}

func (mb MultiBoard) Run() {

	go mb.C.Run(context.Background())
	go mb.StateUpatater()
	if err := ebiten.Run(mb.Update, ScreenWidth, ScreenHeight, 1, "Paint (Ebiten Demo)"); err != nil {
		log.Fatal(err)
	}

}
