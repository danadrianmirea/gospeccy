package spectrum

import (
	"testing"
	"spectrum/formats"
	"spectrum/prettytest"
	"os"
)

const scrFilename = "testdata/screen.scr"

func testMakeVideoMemoryDump(t *prettytest.T) {
	t.Equal(6912, len(speccy.makeVideoMemoryDump()))
}

func TestSaveScreen(t *testing.T) {
	prettytest.Run(
		t,
		beforeAll,
		afterAll,
		testMakeVideoMemoryDump,
	)
}

func testLoadTape(t *prettytest.T) {
	tap, _ := formats.NewTAPFromFile("testdata/hello.tap")
	err := speccy.Load(tap)
	t.Nil(err)

	<-speccy.TapeDrive.loadComplete

	t.True(screenEqualTo("testdata/hello_tap_ok.sna"))
}

func testLoadSnapshot(t *prettytest.T) {
	snapshot, _ := formats.ReadSnapshot("testdata/hello_tap_ok.sna")
	err := speccy.Load(snapshot)
	t.Nil(err)
	t.True(screenEqualTo("testdata/hello_tap_ok.sna"))
}

func TestLoad(t *testing.T) {
	prettytest.Run(
		t,
		before,
		after,
		testLoadTape,
		testLoadSnapshot,
	)
}

func testSystemROMLoaded(t *prettytest.T) {
	systemROMLoaded := make(chan bool)
	speccy.reset(systemROMLoaded)
	t.True(<-systemROMLoaded)
}

func testReset(t *prettytest.T) {
	systemROMLoaded := make(chan bool)
	speccy.reset(systemROMLoaded)
	t.True(<-systemROMLoaded)
}

func TestSystemROMLoaded(t *testing.T) {
	prettytest.Run(
		t,
		before,
		after,
		testSystemROMLoaded,
	)
}

func testMakeVideoMemoryDumpCmd(t *prettytest.T) {
	ch := make(chan []byte)
	speccy.CommandChannel <- Cmd_MakeVideoMemoryDump{ ch }

	data := <-ch

	t.Equal(6912, len(data))
}

func testLoadTapeCmd(t *prettytest.T) {
	errCh := make(chan os.Error)
	program, _ := formats.NewTAPFromFile("testdata/hello.tap")
	speccy.CommandChannel <- Cmd_Load{ ErrChan: errCh, Program: program }
	t.Nil(<-errCh)

	<-speccy.TapeDrive.loadComplete

	t.True(screenEqualTo("testdata/hello_tap_ok.sna"))
}

func testLoadSnapshotCmd(t *prettytest.T) {
	errCh := make(chan os.Error)
	program, _ := formats.ReadSnapshot("testdata/hello_tap_ok.sna")
	speccy.CommandChannel <- Cmd_Load{ ErrChan: errCh, Program: program }
	t.Nil(<-errCh)
	t.True(screenEqualTo("testdata/hello_tap_ok.sna"))
}

func testCheckSystemROMLoadedCmd(t *prettytest.T) {
	systemROMLoaded := make(chan bool)
	speccy.reset(systemROMLoaded)

	speccy.CommandChannel <- Cmd_CheckSystemROMLoaded{}

	t.True(<-systemROMLoaded)
}

func testResetCmd(t *prettytest.T) {
	romLoaded := make(chan bool)
	speccy.CommandChannel <- Cmd_Reset{ romLoaded }
	t.True(<-romLoaded)
}

func TestCommands(t *testing.T) {
	prettytest.Run(
		t,
		before,
		after,
		testMakeVideoMemoryDumpCmd,
		testLoadTapeCmd,
		testLoadSnapshotCmd,
		testCheckSystemROMLoadedCmd,
		testResetCmd,
	)
}
