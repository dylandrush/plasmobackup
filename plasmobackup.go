package main

import (
	"flag"
	"github.com/leemcloughlin/logfile"
	"github.com/rjeczalik/notify"
	"github.com/shirou/gopsutil/process"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

var (
	Debug *log.Logger
	Info  *log.Logger
	Warn  *log.Logger
	Error *log.Logger
)

//TODO: I should prevent multiple instances of this program from running.
// SO basically when i start, instead of lookign for plasmo look for plasmobackup.exe.
// if ones already running stop.

func main() {
	sourcePath := ""
	flag.StringVar(&sourcePath, "s", "", "Source Directory Path")

	outputPath := ""
	flag.StringVar(&outputPath, "o", "", "Output Directory Path")

	debug := false
	flag.BoolVar(&debug, "d", false, "Debug messages to log")

	flag.Parse()

	if sourcePath == "" {
		// No source provided, use a default
		sourcePath = getPlasmoDir()
		if sourcePath == "" {
			log.Panicln("Could not find Plasmo directory.  Make sure it exists or use the -s flag")
		}
		sourcePath = sourcePath + "\\Data"
	}

	if outputPath == "" {
		// No output provided, use a default
		outputPath = getPlasmoDir()
		if outputPath != "" {
			outputPath = filepath.VolumeName(outputPath) + "\\PlasmoMeasurementFiles"
		} else {
			outputPath = "C:\\PlasmoMeasurementFiles"
		}
	}

	if !exists(sourcePath) {
		log.Panicln("The source path: \"", sourcePath, "\" does not exist.  Make sure it exists...")
	}

	if !exists(outputPath) {
		err := os.MkdirAll(outputPath, 0666)
		if err != nil {
			log.Panicln("The output path: \"", outputPath, "\" does not exist.  Make sure it exists...")
		}
	}

	// Initialize the log 10 10MB rotating log
	logFile, err := logfile.New(
		&logfile.LogFile{
			FileName:    outputPath + "\\plasmobackup.log",
			MaxSize:     10000 * 1024,
			OldVersions: 10,
			Flags:       logfile.FileOnly})
	if err != nil {
		log.Panicf("Failed to create logFile %s\\%s: %s\n", outputPath, "plasmobackup.log", err)
	}
	if debug {
		loggerInit(logFile, logFile, logFile, logFile)
	} else {
		loggerInit(ioutil.Discard, logFile, logFile, logFile)
	}

	// Start copying things in a seperate thread that haven't been copied yet.
	go func() {
		if err := filepath.Walk(sourcePath, func(path string, info os.FileInfo, err error) error {
			// TODO check if path exists in the output directory
			target := strings.Replace(path, sourcePath, outputPath, 1)
			if exists(target) {
				Debug.Printf("Skipping file copy: Source file \"%s\" already exists at \"%s\"\n", path, target)
				return nil
			}
			err = copyFile(path, sourcePath, outputPath)
			if err != nil {
				Error.Printf("Could not copy file from \"%s\" to \"%s\"\n", path, target)
			} else {
				Info.Printf("Initial copy of \"%s\" to \"%s\"\n", path, target)
			}
			return nil
		}); err != nil {
			// We should never get an error here as my walk function doesn't actually return one
			Error.Panicf("We tried to copy stuff, but some how failed and I'm not sure how!\n%s\n", err)
		}
	}()

	// Log when plasmo starts and stops.
	plasmoChan := make(chan bool)
	go isPlasmoRunning(plasmoChan)
	go func() {
		for {
			select {
			case isRunning := <-plasmoChan:
				if isRunning {
					Info.Println("Plasmo is running.")
				} else {
					Info.Println("Plasmo has stopped.")
				}
			}
		}
	}()

	eventChan := make(chan notify.EventInfo, 1)
	if err := notify.Watch(sourcePath+"\\...", eventChan, notify.Write); err != nil {
		Error.Panicf("Could not watch directory \"%s\": %s\n", sourcePath, err)
	}
	defer notify.Stop(eventChan)
	for {
		eventInfo := <-eventChan
		go func() {
			if err := copyFile(eventInfo.Path(), sourcePath, outputPath); err != nil {
				Error.Printf("Could not copy \"%s\" into \"%s\": %s\n", eventInfo.Path(), outputPath, err)
			} else {
				Info.Printf("Copied \"%s\" into \"%s\"\n", eventInfo.Path(), outputPath)
			}
		}()
	}

}

func isPlasmoRunning(c chan bool) {
	var plasmoProcess *process.Process
	throttle := time.Tick(5000 * time.Millisecond)
	isStarted := false
	for !isStarted {
		allProcs, _ := process.Processes()
		for _, proc := range allProcs {
			if name, _ := proc.Name(); name == "pA5.exe" {
				plasmoProcess = proc
				isStarted = true
				break
			}
		}
		if isStarted {
			debug.FreeOSMemory()
			c <- true
			for {
				isRunning, err := plasmoProcess.IsRunning()
				if err != nil || !isRunning {
					c <- false
					isStarted = false
					break
				}
				debug.FreeOSMemory()
				<-throttle
			}
		}
		debug.FreeOSMemory()
		<-throttle
	}
}

func getPlasmoDir() (plasmoDir string) {
	for _, drive := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		plasmoDir = string(drive) + ":\\PlasmoAdvancedData"
		f, err := os.Open(plasmoDir)
		if err == nil {
			f.Close()
			return plasmoDir
		}
	}
	plasmoDir = ""
	return plasmoDir
}

// exists returns whether the given file or directory exists
func exists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return true
}

func copyFile(source, sourceDir, outputDir string) error {
	// From ab, replace the SourceDir part of the source file path with OutputDir
	newFilePath := strings.Replace(source, sourceDir, outputDir, 1)
	// Make sure the whole new directory structure exists so we can copy
	fi, _ := os.Stat(source)
	if fi.IsDir() {
		err := os.MkdirAll(newFilePath, 0666)
		if err != nil {
			return err
		}
		return nil
	}
	err := os.MkdirAll(filepath.Dir(newFilePath), 0666)
	if err != nil {
		return err
	}
	err = copyFileContents(source, newFilePath)
	if err != nil {
		return err
	}
	return nil
}

func copyFileContents(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer func() {
		cerr := out.Close()
		if err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return
	}
	err = out.Sync()
	return
}

func loggerInit(debug, info, warn, err io.Writer) {
	Debug = log.New(debug, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
	Info = log.New(info, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	Warn = log.New(warn, "WARN: ", log.Ldate|log.Ltime|log.Lshortfile)
	Error = log.New(err, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}
