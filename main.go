package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/0xAX/notificator"
	"github.com/atotto/clipboard"
	"github.com/faiface/pixel"
	"github.com/faiface/pixel/imdraw"
	"github.com/faiface/pixel/pixelgl"
	"github.com/fsnotify/fsnotify"
	"github.com/kbinani/screenshot"
	"github.com/lithammer/shortuuid/v3"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var notify *notificator.Notificator
var watcher *fsnotify.Watcher

var screensPath string
var remoteHost string
var remoteUser string
var sshKeyPath string
var remotePath string
var baseURL string

func main() {
	var err error

	flags()

	// creates a new file watcher
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	exit := make(chan bool)

	go watch()

	if err := watcher.Add(screensPath); err != nil {
		panic(err)
	}

	pixelgl.Run(run)

	<-exit
}

// flags parses flags
func flags() {
	flag.StringVar(&screensPath, "p", "", "Path to where screenshots are saved locally")
	flag.StringVar(&remoteHost, "r", "", "Remote host, e.g. example.com:2003 or 43.56.122.31:22")
	flag.StringVar(&remoteUser, "ru", "", "Username on remote host")
	flag.StringVar(&sshKeyPath, "pk", "", "Private key path")
	flag.StringVar(&remotePath, "rp", "", "Path on the remote host")
	flag.StringVar(&baseURL, "url", "", "A base URL that points to given screenshot, e.g https://i.slacki.io/")
	flag.Parse()

	screensPath = strings.TrimRight(screensPath, "/") + "/"
	remotePath = strings.TrimRight(remotePath, "/") + "/"
	baseURL = strings.TrimRight(baseURL, "/") + "/"
}

func watch() {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				upload()
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				upload()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("error:", err)
		}
	}
}

func upload() {
	fileExtRegexp, _ := regexp.Compile(".*?\\.(\\w+)$")

	fi, err := ioutil.ReadDir(screensPath)
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range fi {
		fmt.Println(f.Name())
		if f.IsDir() {
			continue
		}
		fullPath := screensPath + f.Name()

		matches := fileExtRegexp.FindAllStringSubmatch(f.Name(), -1)

		if len(matches) > 0 && len(matches[0]) > 1 {
			ext := matches[0][1]
			if !allowedExtension(ext) {
				continue
			}
			if ext == "mov" {
				log.Println("Detected .mov file, converting to mp4")
				result := ffmpegTranscode(fullPath, screensPath+"out.mp4")
				if result {
					// remove the .mov file if successfully transcoded
					// next pass will upload the file
					os.Remove(fullPath)
					continue
				}
			}

			remoteFilename := fmt.Sprintf("%s.%s", shortuuid.New(), ext)
			err = uploadObjectToDestination(fullPath, remoteFilename)
			if err != nil {
				log.Println(err)
				continue
			}
			url := baseURL + remoteFilename
			copyToClipboard(url)
			showNotification(url)
			openbrowser(url)
			os.Remove(fullPath)
		}

	}
}

// showNotification displays a system notification about uploaded screenshot
func showNotification(url string) {
	notify = notificator.New(notificator.Options{
		AppName: "Skrins",
	})
	notify.Push("Screenshot uploaded!", url, "", notificator.UR_NORMAL)
}

// copyToClipboard puts a string to clipboards
func copyToClipboard(s string) {
	clipboard.WriteAll(s)
}

// allowedExtension determines whether it is allowed to upload a file with that extension
func allowedExtension(ext string) bool {
	allowed := []string{"jpg", "jpeg", "png", "gif", "webm", "mp4", "mov", "zip", "tar", "tar.gz", "tar.bz2"}

	for _, e := range allowed {
		if ext == e {
			return true
		}
	}

	return false
}

// ffmpegTranscode transcodes a media file.
func ffmpegTranscode(fileIn, fileOut string) bool {
	cmd := exec.Command("/usr/local/bin/ffmpeg", "-i", fileIn, fileOut)
	var stderr bytes.Buffer
	var stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()

	if err != nil {
		log.Println(err)
		return false
	}
	log.Println("[ffmpeg stderr]", stderr.String())
	log.Println("[ffmpeg stdout]", stdout.String())

	return true
}

// newSFTPClient creates new sFTP client
func newSFTPClient() (*sftp.Client, error) {
	key, err := ioutil.ReadFile(sshKeyPath)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: remoteUser,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", remoteHost, config)
	if err != nil {
		return nil, err
	}
	return sftp.NewClient(client)
}

// uploadObjectToDestination uploads file to a remote host
func uploadObjectToDestination(src, dest string) error {
	client, err := newSFTPClient()
	if err != nil {
		return err
	}
	defer client.Close()

	// create destination file
	// remotePath is expected to have a trailing slash
	dstFile, err := client.OpenFile(remotePath+dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// open local file
	srcReader, err := os.Open(src)
	if err != nil {
		return err
	}

	// copy source file to destination file
	bytes, err := io.Copy(dstFile, srcReader)
	if err != nil {
		return err
	}

	log.Printf("Total of %d bytes copied\n", bytes)

	return nil
}

func run() {
	monitorX, monitorY := pixelgl.PrimaryMonitor().Size()
	cfg := pixelgl.WindowConfig{
		Bounds:                 pixel.R(0, 0, monitorX, monitorY),
		Undecorated:            true,
		TransparentFramebuffer: true,
	}
	win, err := pixelgl.NewWindow(cfg)
	if err != nil {
		panic(err)
	}

	win.Clear(color.RGBA{
		R: 0,
		G: 0,
		B: 0,
		A: 10,
	})

	var u = pixel.V(0.0, 0.0)
	var v = pixel.V(0.0, 0.0)
	drawing := false
	imd := imdraw.New(nil)
	var rect pixel.Rect
	shot := false
	for !win.Closed() {
		if win.Pressed(pixelgl.MouseButtonLeft) {
			win.Clear(color.RGBA{
				R: 0,
				G: 0,
				B: 0,
				A: 20,
			})

			if !drawing {
				u = win.MousePosition()
				drawing = true
			} else {
				v = win.MousePosition()

				rect = pixel.R(u.X, u.Y, v.X, v.Y)

				imd.Clear()
				imd.Color = color.RGBA{
					R: 225,
					G: 225,
					B: 225,
					A: 10,
				}
				imd.Push(rect.Min, rect.Max)
				imd.Rectangle(1)
			}
		}

		if win.JustReleased(pixelgl.MouseButtonLeft) {
			drawing = false
			win.SetClosed(true)
			shot = true
		}

		if win.JustReleased(pixelgl.MouseButtonRight) {
			win.SetClosed(true)
		}

		imd.Draw(win)
		win.Update()
	}
	win.Destroy()
	if shot {
		maxX := int(rect.Max.X)
		maxY := int(rect.Max.Y)
		minX := int(rect.Min.X)
		minY := int(rect.Min.Y)
		maxY = int(monitorY) - maxY + 24 // 24 pixel offset?
		minY = int(monitorY) - minY + 24 // 24 pixel offset?
		fmt.Printf("%d %d %d %d\n", minX, minY, maxX, maxY)
		if minX > maxX {
			tmpMax := maxX
			maxX = minX
			minX = tmpMax
		}
		if minY > maxY {
			tmpMax := maxY
			maxY = minY
			minY = tmpMax
		}
		generateScreenshot(image.Rectangle{
			Min: image.Point{
				X: minX,
				Y: minY,
			},
			Max: image.Point{
				X: maxX,
				Y: maxY,
			},
		})
	}
}

func generateScreenshot(bounds image.Rectangle) {
	// TODO how to support multiple displays?
	fmt.Printf("%v\n", bounds)
	fmt.Printf("%v\n", screenshot.GetDisplayBounds(0))
	img, err := screenshot.CaptureRect(bounds)
	if err != nil {
		panic(err)
	}
	fileName := fmt.Sprintf("%s%d_%dx%d.png", screensPath, time.Now().Unix(), bounds.Dx(), bounds.Dy())
	file, _ := os.Create(fileName)
	defer file.Close()
	png.Encode(file, img)

	fmt.Printf("# %v \"%s\"\n", bounds, fileName)
}

// openbrowser opens a url in the default browser
func openbrowser(url string) {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Fatal(err)
	}

}
