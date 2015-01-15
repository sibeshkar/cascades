package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cascades-fbp/cascades/components/utils"
	"github.com/cascades-fbp/cascades/runtime"
	zmq "github.com/pebbe/zmq4"
)

var (
	// Flags
	inputEndpoint  = flag.String("port.dir", "", "Component's input port endpoint")
	outputEndpoint = flag.String("port.file", "", "Component's output port endpoint")
	errorEndpoint  = flag.String("port.err", "", "Component's error port endpoint")
	jsonFlag       = flag.Bool("json", false, "Print component documentation in JSON")
	debug          = flag.Bool("debug", false, "Enable debug mode")

	// Internal
	inPort, outPort, errPort *zmq.Socket
	inCh, outCh, errCh       chan bool
	ip                       [][]byte
	err                      error
)

func validateArgs() {
	if *inputEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}
	if *outputEndpoint == "" {
		flag.Usage()
		os.Exit(1)
	}
}

func openPorts() {
	inPort, err = utils.CreateInputPort("fs/walk.dir", *inputEndpoint, inCh)
	utils.AssertError(err)

	outPort, err = utils.CreateOutputPort("fs/walk.file", *outputEndpoint, outCh)
	utils.AssertError(err)

	if *errorEndpoint != "" {
		errPort, err = utils.CreateOutputPort("fs/walk.err", *errorEndpoint, errCh)
		utils.AssertError(err)
	}
}

func closePorts() {
	inPort.Close()
	outPort.Close()
	if errPort != nil {
		errPort.Close()
	}
	zmq.Term()
}

func main() {
	flag.Parse()

	if *jsonFlag {
		doc, _ := registryEntry.JSON()
		fmt.Println(string(doc))
		os.Exit(0)
	}

	log.SetFlags(0)
	if *debug {
		log.SetOutput(os.Stdout)
	} else {
		log.SetOutput(ioutil.Discard)
	}

	validateArgs()

	ch := utils.HandleInterruption()
	inCh = make(chan bool)
	outCh = make(chan bool)
	errCh = make(chan bool)

	openPorts()
	defer closePorts()

	ports := 1
	if outPort != nil {
		ports++
	}
	if errPort != nil {
		ports++
	}

	waitCh := make(chan bool)
	inExitCh := make(chan bool, 1)
	go func(num int) {
		total := 0
		for {
			select {
			case v := <-inCh:
				if v {
					total++
				} else {
					inExitCh <- true
				}
			case v := <-outCh:
				if !v {
					log.Println("OUT port is closed. Interrupting execution")
					ch <- syscall.SIGTERM
				} else {
					total++
				}
			case v := <-errCh:
				if !v {
					log.Println("ERR port is closed. Interrupting execution")
					ch <- syscall.SIGTERM
				} else {
					total++
				}
			}
			if total >= num && waitCh != nil {
				waitCh <- true
			}
		}
	}(ports)

	log.Println("Waiting for port connections to establish... ")
	select {
	case <-waitCh:
		log.Println("Ports connected")
		waitCh = nil
	case <-time.Tick(30 * time.Second):
		log.Println("Timeout: port connections were not established within provided interval")
		os.Exit(1)
	}

	log.Println("Started...")
	for {
		ip, err := inPort.RecvMessageBytes(zmq.DONTWAIT)
		if err != nil {
			select {
			case <-inExitCh:
				log.Println("DIR port is closed. Interrupting execution")
				ch <- syscall.SIGTERM
				break
			default:
				// IN port is still open
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if !runtime.IsValidIP(ip) {
			continue
		}
		dir := string(ip[1])
		err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				outPort.SendMessage(runtime.NewPacket([]byte(path)))
			}
			return nil
		})
		if err != nil {
			log.Printf("ERROR openning file %s: %s", dir, err.Error())
			if errPort != nil {
				errPort.SendMessage(runtime.NewPacket([]byte(err.Error())))
			}
			continue
		}

		select {
		case <-inCh:
			log.Println("IN port is closed. Interrupting execution")
			ch <- syscall.SIGTERM
			break
		default:
			// IN port is still open
		}
	}
}
