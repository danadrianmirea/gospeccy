package spectrum

import (
	"spectrum/formats"
	"fmt"
	"io/ioutil"
	"os"
	"container/vector"
)

const TStatesPerFrame = 69888 // Number of T-states per frame
const InterruptLength = 32    // How long does an interrupt last in T-states
const DefaultFPS = 50.08

type DisplayInfo struct {
	displayReceiver DisplayReceiver

	// The index of the last frame sent to the 'displayReceiver', initially nil.
	lastFrame *uint

	// Total number of frames that the DisplayReceiver did not receive
	// because the send might block CPU emulation
	numMissedFrames uint

	missedChanges *DisplayData
}

type Spectrum48k struct {
	Cpu      *Z80
	Memory   MemoryAccessor
	ula      *ULA
	Keyboard *Keyboard
	TapeDrive *TapeDrive

	Ports    PortAccessor

	romPath string

	// The current display refresh frequency.
	// The initial value if 'DefaultFPS'.
	// It is always greater than 0.
	currentFPS float

	// A value received from this channel indicates the new display refresh frequency.
	// By default, this channel initially receives the value 'DefaultFPS'.
	FPS <-chan float
	fps chan float

	// A value received from this channel indicates that the
	// system ROM has been loaded. This applies to the standard
	// 48k ROM but probably doesn't work with custom ROMs.
	systemROMLoaded chan bool

	CommandChannel chan<- interface{}
	commandChannel <-chan interface{}

	// True if the system ROM has been loaded
	romNotYetLoaded bool

	// A vector of '*DisplayInfo', initially empty
	displays vector.Vector

	// A vector of 'AudioReceiver', initially empty
	audioReceivers vector.Vector

	app *Application
}

// Create a new speccy object.
func NewSpectrum48k(app *Application, romPath string) (*Spectrum48k, os.Error) {
	memory := NewMemory()
	keyboard := NewKeyboard()
	ports := NewPorts()
	z80 := NewZ80(memory, ports)
	ula := NewULA()

	tapeDrive := NewTapeDrive()

	speccy := &Spectrum48k{
		Cpu:            z80,
		Memory:         memory,
		ula:            ula,
		Keyboard:       keyboard,
		Ports:          ports,
		romPath:        romPath,
		displays:       vector.Vector{},
		audioReceivers: vector.Vector{},
		app:            app,
   	        TapeDrive: tapeDrive,
	}

	memory.init(speccy)
	keyboard.init(speccy)
	z80.init(speccy)
	ula.init(speccy)
	ports.init(speccy)
	tapeDrive.init(speccy)

	err := speccy.reset(make(chan bool))

	if err != nil {
		return nil, err
	}

	speccy.currentFPS = DefaultFPS
	speccy.fps = make(chan float, 1)
	speccy.FPS = speccy.fps
	speccy.fps <- DefaultFPS

	commandChannel := make(chan interface{})
	speccy.CommandChannel = commandChannel
	speccy.commandChannel = commandChannel
	go commandLoop(speccy)

	return speccy, nil
}

type Cmd_Reset struct { 
	// This channel will receive true when the system ROM has been
	// loaded
	SystemROMLoaded chan bool
}
type Cmd_RenderFrame struct {
	// This channel (if not nil) will receive the time when the WHOLE rendering finished.
	// The time is obtained via a call to time.Nanoseconds().
	CompletionTime_orNil chan<- int64
}
type Cmd_GetNumDisplayReceivers struct {
	N chan<- uint
}
type Cmd_AddDisplay struct {
	Display DisplayReceiver
}
type Cmd_CloseAllDisplays struct {
	Finished chan<- byte
}
type Cmd_SetFPS struct {
	NewFPS float
}
type Cmd_SetUlaEmulationAccuracy struct {
	AccurateEmulation bool
}
type Cmd_GetNumAudioReceivers struct {
	N chan<- uint
}
type Cmd_AddAudioReceiver struct {
	Receiver AudioReceiver
}
type Cmd_CloseAllAudioReceivers struct {
	Finished chan<- byte
}
type Cmd_LoadSnapshot struct {
	InformalFilename string // This is only used for logging purposes
	Snapshot         formats.Snapshot
	ErrChan          chan<- os.Error
}
type Cmd_Load struct {
	InformalFilename string // This is only used for logging purposes
	Program         interface{}
	ErrChan          chan<- os.Error
}
type Cmd_MakeSnapshot struct {
	Chan chan<- *formats.FullSnapshot
}
type Cmd_MakeVideoMemoryDump struct {
	Chan chan<- []byte
}
type Cmd_KeyboardReadState struct {
	Chan chan rowState
}
type Cmd_CheckSystemROMLoaded struct {}

func commandLoop(speccy *Spectrum48k) {
	evtLoop := speccy.app.NewEventLoop()
	for {
		select {
		case <-evtLoop.Pause:
			speccy.Close()
			evtLoop.Pause <- 0

		case <-evtLoop.Terminate:
			// Terminate this Go routine
			if evtLoop.App().Verbose {
				evtLoop.App().PrintfMsg("command loop: exit")
			}
			evtLoop.Terminate <- 0
			return
		case untyped_cmd := <-speccy.commandChannel:
			switch cmd := untyped_cmd.(type) {
			case Cmd_Reset:
				speccy.reset(cmd.SystemROMLoaded)

			case Cmd_RenderFrame:
				speccy.renderFrame(cmd.CompletionTime_orNil)
				
			case Cmd_CheckSystemROMLoaded:
				// Ugly hack to check whenever the
				// system ROM has been loaded after a
				// reset. I bet this won't work with
				// custom ROMs.
				if speccy.Cpu.PC() == 0x10ac && speccy.romNotYetLoaded {
					speccy.systemROMLoaded <- true
					speccy.romNotYetLoaded = false
				}

			case Cmd_GetNumDisplayReceivers:
				cmd.N <- uint(speccy.displays.Len())

			case Cmd_AddDisplay:
				speccy.addDisplay(cmd.Display)

			case Cmd_CloseAllDisplays:
				go func() {
					speccy.closeAllDisplays()
					cmd.Finished <- 0
				}()

			case Cmd_SetFPS:
				speccy.setFPS(cmd.NewFPS)

			case Cmd_SetUlaEmulationAccuracy:
				speccy.ula.setEmulationAccuracy(cmd.AccurateEmulation)

			case Cmd_GetNumAudioReceivers:
				cmd.N <- uint(speccy.audioReceivers.Len())

			case Cmd_AddAudioReceiver:
				speccy.addAudioReceiver(cmd.Receiver)

			case Cmd_CloseAllAudioReceivers:
				go func() {
					speccy.closeAllAudioReceivers()
					cmd.Finished <- 0
				}()

			case Cmd_LoadSnapshot:
				if speccy.app.Verbose {
					if len(cmd.InformalFilename) > 0 {
						speccy.app.PrintfMsg("loading snapshot \"%s\"", cmd.InformalFilename)
					} else {
						speccy.app.PrintfMsg("loading a snapshot")
					}
				}

				err := speccy.loadSnapshot(cmd.Snapshot)

				if cmd.ErrChan != nil {
					cmd.ErrChan <- err
				}

			case Cmd_Load:
				if speccy.app.Verbose {
					if len(cmd.InformalFilename) > 0 {
						speccy.app.PrintfMsg("loading program \"%s\"", cmd.InformalFilename)
					} else {
						speccy.app.PrintfMsg("loading a program")
					}
				}

				err := speccy.Load(cmd.Program)

				if cmd.ErrChan != nil {
					cmd.ErrChan <- err
				}


			case Cmd_MakeSnapshot:
				cmd.Chan <- speccy.Cpu.makeSnapshot()

			case Cmd_MakeVideoMemoryDump:
				cmd.Chan <- speccy.makeVideoMemoryDump()

			}
		}
	}
}

func (speccy *Spectrum48k) reset(systemROMLoaded chan bool) os.Error {
	speccy.Cpu.reset()
	speccy.Memory.reset()
	speccy.ula.reset()
	speccy.Keyboard.reset()
	speccy.Ports.reset()

	speccy.romNotYetLoaded = true

	speccy.systemROMLoaded = systemROMLoaded

	// Load the first 16k of memory with the ROM image
	{
		rom48k, err := ioutil.ReadFile(speccy.romPath)
		if err != nil {
			return err
		}
		if len(rom48k) != 0x4000 {
			return os.NewError(fmt.Sprintf("ROM file \"%s\" has an invalid size", speccy.romPath))
		}

		for address, b := range rom48k {
			speccy.Memory.Write(uint16(address), b)
		}
	}

	return nil
}

func (speccy *Spectrum48k) SystemROMLoaded() chan bool {
	return speccy.systemROMLoaded
}

func (speccy *Spectrum48k) Close() {
	speccy.Cpu.close()

	if speccy.app.Verbose {
		eff := speccy.Cpu.GetEmulationEfficiency()
		if eff != 0 {
			speccy.app.PrintfMsg("emulation efficiency: %d host-CPU instructions per Z80 instruction", eff)
		} else {
			speccy.app.PrintfMsg("emulation efficiency: -")
		}

		for i, display := range speccy.displays {
			speccy.app.PrintfMsg("display #%d: %d missed frames", i, display.(*DisplayInfo).numMissedFrames)
		}
	}
}

func (speccy *Spectrum48k) addDisplay(display DisplayReceiver) {
	d := &DisplayInfo{
		displayReceiver: display,
		lastFrame:       nil,
		numMissedFrames: 0,
		missedChanges:   nil,
	}

	speccy.displays.Push(d)
}

func (speccy *Spectrum48k) closeAllDisplays() {
	var displays vector.Vector
	{
		displays = speccy.displays
		speccy.displays = vector.Vector{}
	}

	for i, d := range displays {
		d.(*DisplayInfo).displayReceiver.close()
		if speccy.app.Verbose {
			speccy.app.PrintfMsg("display #%d: %d missed frames", i, d.(*DisplayInfo).numMissedFrames)
		}
	}
}

func (speccy *Spectrum48k) setFPS(newFPS float) {
	if newFPS <= 1.0 {
		newFPS = DefaultFPS
	}

	if newFPS != speccy.currentFPS {
		speccy.currentFPS = newFPS
		speccy.fps <- newFPS
	}
}

func (speccy *Spectrum48k) addAudioReceiver(receiver AudioReceiver) {
	speccy.audioReceivers.Push(receiver)
}

func (speccy *Spectrum48k) closeAllAudioReceivers() {
	var audioReceivers vector.Vector
	{
		audioReceivers = speccy.audioReceivers
		speccy.audioReceivers = vector.Vector{}
	}

	for _, r := range audioReceivers {
		r.(AudioReceiver).close()
	}
}

var frames uint64

func (speccy *Spectrum48k) renderFrame(completionTime_orNil chan<- int64) {
	speccy.Ports.frame_begin()
	speccy.ula.frame_begin()

	// Execute instructions corresponding to one screen frame
	speccy.Cpu.tstates = (speccy.Cpu.tstates % TStatesPerFrame)
	speccy.Cpu.interrupt()
	speccy.Cpu.eventNextEvent = TStatesPerFrame
	speccy.Cpu.doOpcodes()

	// Send display data to display backend(s)
	{
		firstDisplay := true
		for _, display := range speccy.displays {
			var tm chan<- int64
			if firstDisplay {
				tm = completionTime_orNil
			} else {
				tm = nil
			}
			speccy.ula.sendScreenToDisplay(display.(*DisplayInfo), tm)
			firstDisplay = false
		}
	}

	// Send audio data to audio backend(s)
	{
		audioData := AudioData{
			fps:                speccy.currentFPS,
			beeperEvents_orNil: speccy.Ports.getBeeperEvents_orNil(),
		}

		for _, audioReceiver := range speccy.audioReceivers {
			audioReceiver.(AudioReceiver).getAudioDataChannel() <- &audioData
		}
	}

	speccy.Ports.frame_end()
}


// Initializes state from the specified snapshot.
// Returns nil on success.
func (speccy *Spectrum48k) loadSnapshot(s formats.Snapshot) os.Error {
	err := speccy.reset(make(chan bool))
	if err != nil {
		return err
	}

	err = speccy.Cpu.loadSnapshot(s)
	if err != nil {
		return err
	}

	return nil
}

// Load the given tape. Returns nil on success.
func (speccy *Spectrum48k) loadTape(tap *formats.TAP) {
	speccy.TapeDrive.Insert(NewTape(tap))
	speccy.TapeDrive.Stop()
	speccy.sendLOADCommand()
	speccy.TapeDrive.Play()	
}

// Send LOAD ""
func (speccy *Spectrum48k) sendLOADCommand() {
	speccy.Keyboard.CommandChannel <- Cmd_SendLoad{}
}

func (speccy *Spectrum48k) makeVideoMemoryDump() []byte {
	return speccy.Memory.Data()[0x4000:0x4000+6912]
}

func (speccy *Spectrum48k) GetCurrentFPS() float {
	return speccy.currentFPS
}

func (speccy *Spectrum48k) Load(program interface{}) os.Error {
	var err os.Error

	switch program := program.(type) {

	case formats.Snapshot:
		speccy.loadSnapshot(program.(formats.Snapshot))
	case *formats.TAP:
		speccy.loadTape(program)
	default:
		err = os.NewError("Invalid program type.")
		return err
	}

	return err
}


