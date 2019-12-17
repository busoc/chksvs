package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"
)

const (
	magic = "SVS "
	bad   = ".bad"
)

const (
	DefaultWorkers = 4
	DefaultFiles   = 512
)

type UPI [32]byte

func (u UPI) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	xs := u[:]
	xs = bytes.Trim(xs, "\x00")
	return e.EncodeElement(string(xs), xml.StartElement{Name: xml.Name{Local: "upi"}})
}

type Timestamp uint64

func (t Timestamp) String() string {
	return t.Time().Format("2006-01-02T15:04:05.999999999Z")
}

func (t Timestamp) Time() time.Time {
	return GPS.Add(time.Duration(t)).UTC()
}

func (t Timestamp) MarshalXMLAttr(n xml.Name) (xml.Attr, error) {
	a := xml.Attr{
		Name:  n,
		Value: t.String(),
	}
	return a, nil
}

func (t Timestamp) MarshalXML(e *xml.Encoder, _ xml.StartElement) error {
	str := t.String()
	return e.EncodeElement(str, xml.StartElement{Name: xml.Name{Local: "acquisition-time"}})
}

type Metadata struct {
	Magic       uint8     `xml:"-"`
	Acquisition Timestamp `xml:"acquisition-time"`
	Sequence    uint32    `xml:"originator-seq-no"`
	Auxiliary   uint64    `xml:"auxiliary-time"`
	Source      uint8     `xml:"originator-id"`
	X           uint16    `xml:"source-x-size"`
	Y           uint16    `xml:"source-y-size"`
	Format      uint8     `xml:"format"`
	Drop        uint16    `xml:"fdrp"`
	OffsetX     uint16    `xml:"roi-x-offset"`
	SizeX       uint16    `xml:"roi-x-size"`
	OffsetY     uint16    `xml:"roi-y-offset"`
	SizeY       uint16    `xml:"roi-y-size"`
	ScaleX      uint16    `xml:"scale-x-size"`
	ScaleY      uint16    `xml:"scale-y-size"`
	Ratio       uint8     `xml:"scale-far"`
	UPI         UPI       `xml:"user-packet-info"`
}

var GPS = time.Date(1980, 1, 6, 0, 0, 0, 0, time.UTC)

func ini() {
	log.SetOutput(os.Stdout)
	log.SetFlags(0)
}

func main() {
	var (
		datadir = flag.String("d", os.TempDir(), "datadir")
		keepbad = flag.Bool("k", false, "keep-bad")
		per     = flag.Int64("p", DefaultFiles, "files per directory")
		workers = flag.Int64("w", DefaultWorkers, "workers")
	)
	flag.Parse()

	if *per <= 0 {
		*per = DefaultFiles
	}
	if *workers <= 0 {
		*workers = DefaultWorkers
	}

	if err := os.MkdirAll(*datadir, 0755); err != nil && !os.IsExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(12)
	}

	var (
		fid  int
		ctx  = context.TODO()
		sema = semaphore.NewWeighted(*workers)
	)
	for f := range iterFiles(flag.Args(), *keepbad) {
		if err := sema.Acquire(ctx, 1); err != nil {
			log.Println(err)
			os.Exit(1)
		}
		go func(f string, i int) {
			defer sema.Release(1)
			file, err := processFile(f, *datadir, *per)
			if err != nil {
				log.Println(f, err)
				return
			}
			if file != "" {
				log.Printf("%6d: processing %s -> %s", i+1, f, file)
			}
		}(f, fid)
		fid++
	}
	if err := sema.Acquire(ctx, *workers); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}

func processFile(file, datadir string, per int64) (string, error) {
	r, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer r.Close()

	meta := struct {
		FCC  [4]byte
		Seq  uint32
		When Timestamp
	}{}
	if err := binary.Read(r, binary.BigEndian, &meta); err != nil {
		return "", err
	}
	if !bytes.Equal(meta.FCC[:], []byte(magic)) {
		return "", nil
	}

	var (
		parts = strings.Split(filepath.Base(file), "_")
		upi   = strings.Join(parts[1:len(parts)-5], "_")
	)
	datadir = filepath.Join(datadir, upi)

	if err := os.MkdirAll(datadir, 0755); err != nil {
		return "", err
	}

	if meta.Seq == 1 {
		return processIntro(r, datadir, upi)
	}
	file, per, err = processMeta(r, datadir, upi, file, per, meta.Seq, meta.When)
	if err == nil {
		file = filepath.Join(datadir, fmt.Sprintf("%06d", per), file)
		err = processData(r, file)
	}
	return file, err
}

func processIntro(r io.Reader, datadir, upi string) (string, error) {
	w, err := os.Create(filepath.Join(datadir, upi+".ini"))
	if err != nil {
		return "", err
	}
	defer w.Close()

	_, err = io.Copy(w, r)
	return w.Name(), err
}

func processMeta(r io.Reader, datadir, upi, source string, per int64, seq uint32, when Timestamp) (string, int64, error) {
	var info Metadata
	if err := binary.Read(r, binary.LittleEndian, &info); err != nil {
		return "", -1, err
	}
	var (
		acqt   = info.Acquisition.Time()
		file   = fmt.Sprintf("%04x_%s_%s_%06d.csv", info.Source, upi, acqt.Format("20060102_150406"), info.Sequence)
		subdir = int64(info.Sequence) / per
	)

	datadir = filepath.Join(datadir, fmt.Sprintf("%06d", subdir))
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return "", subdir, err
	}

	w, err := os.Create(filepath.Join(datadir, file) + ".xml")
	if err != nil {
		return "", subdir, err
	}
	defer w.Close()

	elem := struct {
		XMLName xml.Name  `xml:"metadata"`
		When    Timestamp `xml:"svs-timestamp,attr"`
		Seq     uint32    `xml:"svs-sequence,attr"`
		Source  string    `xml:"svs-file,attr"`
		Metadata
	}{
		When:     when,
		Seq:      seq,
		Source:   filepath.Base(source),
		Metadata: info,
	}

	e := xml.NewEncoder(w)
	e.Indent("", "\t")
	return file, subdir, e.Encode(elem)
}

func processData(r io.Reader, file string) error {
	w, err := os.Create(file)
	if err != nil {
		return err
	}
	defer w.Close()

	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	var (
		ws = csv.NewWriter(w)
		rs = bytes.NewReader(buf)
	)

	b, err := rs.ReadByte()
	if err != nil {
		return err
	}
	vs := make([]string, int(b)+1)
	vs[0] = "t"
	for j := 0; j < int(b); j++ {
		var v uint16
		if err := binary.Read(rs, binary.LittleEndian, &v); err != nil {
			return err
		}
		vs[j+1] = fmt.Sprintf("g2(t, %d)", v)
	}
	ws.Write(vs)
	for i := 0; rs.Len() > 0; i++ {
		vs[0] = strconv.Itoa(i)
		for j := 0; j < int(b); j++ {
			var v float32
			if err := binary.Read(rs, binary.LittleEndian, &v); err != nil {
				return err
			}
			vs[j+1] = strconv.FormatFloat(float64(v), 'f', -1, 32)
		}
		ws.Write(vs)
	}

	ws.Flush()
	return ws.Error()
}

func iterFiles(files []string, keepbad bool) <-chan string {
	if len(files) == 0 {
		return readFiles(keepbad)
	}
	return walkFiles(files, keepbad)
}

func readFiles(keepbad bool) <-chan string {
	queue := make(chan string)
	go func() {
		defer close(queue)
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			f := s.Text()
			if f == "" {
				continue
			}
			filepath.Walk(f, func(f string, i os.FileInfo, err error) error {
				if err != nil || i.IsDir() {
					return err
				}
				if filepath.Ext(f) == bad && !keepbad {
					return nil
				}
				queue <- f
				return nil
			})
		}
	}()
	return queue
}

func walkFiles(files []string, keepbad bool) <-chan string {
	queue := make(chan string)
	go func() {
		defer close(queue)
		for _, f := range files {
			filepath.Walk(f, func(f string, i os.FileInfo, err error) error {
				if err != nil || i.IsDir() {
					return err
				}
				if filepath.Ext(f) == bad && !keepbad {
					return nil
				}
				queue <- f
				return nil
			})
		}
	}()
	return queue
}
