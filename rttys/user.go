package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"rttys/client"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	loginErrorNone    = 0x00
	loginErrorOffline = 0x01
	loginErrorBusy    = 0x02
)

type user struct {
	br     *broker
	sid    string
	devid  string
	conn   *websocket.Conn
	closed bool
	close  sync.Once
	send   chan *usrMessage // Buffered channel of outbound messages.
}

type usrMessage struct {
	sid  string
	typ  int
	data []byte
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (u *user) IsDevice() bool {
	return false
}

func (u *user) DeviceID() string {
	return u.devid
}

func (u *user) WriteMsg(typ int, data []byte) {
	u.send <- &usrMessage{
		typ:  typ,
		data: data,
	}
}

func (u *user) Closed() bool {
	return u.closed
}

func (u *user) CloseConn() {
	u.conn.Close()
}

func (u *user) Close() {
	u.close.Do(func() {
		u.closed = true
		u.CloseConn()
		close(u.send)
	})
}

func userLoginAck(code int, c client.Client) {
	msg := fmt.Sprintf(`{"type":"login","sid":"%s","err":%d}`, c.(*user).sid, code)
	c.WriteMsg(websocket.TextMessage, []byte(msg))
}

func (u *user) readLoop() {
	defer func() {
		u.br.unregister <- u
	}()

	for {
		typ, data, err := u.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Msg(err.Error())
			}
			break
		}

		u.br.userMessage <- &usrMessage{u.sid, typ, data}
	}
}

func (u *user) writeLoop() {
	ticker := time.NewTicker(time.Second * 5)

	defer func() {
		ticker.Stop()
		u.br.unregister <- u
	}()

	for {
		var err error

		select {
		case <-ticker.C:
			err = u.conn.WriteMessage(websocket.PingMessage, []byte{})

		case msg, ok := <-u.send:
			if !ok {
				return
			}

			err = u.conn.WriteMessage(msg.typ, msg.data)
		}

		if err != nil {
			log.Error().Msg(err.Error())
			return
		}
	}
}

func serveUser(br *broker, c *gin.Context) {
	devid := c.Param("devid")
	if devid == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.Status(http.StatusBadRequest)
		log.Error().Msg(err.Error())
		return
	}

	u := &user{
		br:    br,
		conn:  conn,
		devid: devid,
		send:  make(chan *usrMessage, 256),
	}

	go u.readLoop()
	go u.writeLoop()

	br.register <- u
}
