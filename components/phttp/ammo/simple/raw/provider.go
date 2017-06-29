package raw

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"

	"github.com/facebookgo/stackerr"
	"github.com/spf13/afero"

	"github.com/yandex/pandora/components/phttp/ammo/simple"
)

/*
Parses size-prefixed HTTP ammo files. Each ammo is prefixed with a header line (delimited with \n), which consists of
two fields delimited by a space: ammo size and tag. Ammo size is in bytes (integer, including special characters like CR, LF).
Tag is a string. Example:

77 bad
GET /abra HTTP/1.0
Host: xxx.tanks.example.com
User-Agent: xxx (shell 1)

904
POST /upload/2 HTTP/1.0
Content-Length: 801
Host: xxxxxxxxx.dev.example.com
User-Agent: xxx (shell 1)

^.^........W.j^1^.^.^.²..^^.i.^B.P..-!(.l/Y..V^.      ...L?...S'NR.^^vm...3Gg@s...d'.\^.5N.$NF^,.Z^.aTE^.
._.[..k#L^ƨ`\RE.J.<.!,.q5.F^՚iΔĬq..^6..P..тH.`..i2
.".uuzs^^F2...Rh.&.U.^^..J.P@.A......x..lǝy^?.u.p{4..g...m.,..R^.^.^......].^^.^J...p.ifTF0<.s.9V.o5<..%!6ļS.ƐǢ..㱋....C^&.....^.^y...v]^YT.1.#K.ibc...^.26...   ..7.
b.$...j6.٨f...W.R7.^1.3....K`%.&^..d..{{      l0..^\..^X.g.^.r.(!.^^...4.1.$\ .%.8$(.n&..^^q.,.Q..^.D^.].^.R9.kE.^.$^.I..<..B^..^.h^^C.^E.|....3o^.@..Z.^.s.$[v.
527
POST /upload/3 HTTP/1.0
Content-Length: 424
Host: xxxxxxxxx.dev.example.com
User-Agent: xxx (shell 1)

^.^........QMO.0^.++^zJw.ر^$^.^Ѣ.^V.J....vM.8r&.T+...{@pk%~C.G../z顲^.7....l...-.^W"cR..... .&^?u.U^^.^.....{^.^..8.^.^.I.EĂ.p...'^.3.Tq..@R8....RAiBU..1.Bd*".7+.
.Ol.j=^.3..n....wp..,Wg.y^.T..~^..
*/

func filePosition(file afero.File) (position int64) {
	position, _ = file.Seek(0, os.SEEK_CUR)
	return
}

type Config struct {
	File string `validate:"required"`
	// Limit limits total num of ammo. Unlimited if zero.
	Limit int `validate:"min=0"`
	// Passes limits ammo file passes. Unlimited if zero.
	Passes int `validate:"min=0"`
}

// TODO: pass logger and metricsRegistry
func NewProvider(fs afero.Fs, conf Config) *Provider {
	var p Provider
	p = Provider{
		Provider: simple.NewProvider(fs, conf.File, p.start),
		Config:   conf,
	}
	return &p
}

type Provider struct {
	simple.Provider
	Config
}

func (p *Provider) start(ctx context.Context, ammoFile afero.File) error {
	var passNum int
	var ammoNum int
	for {
		passNum++
		reader := bufio.NewReader(ammoFile)
		for p.Limit == 0 || ammoNum < p.Limit {
			data, isPrefix, err := reader.ReadLine()
			if isPrefix {
				return stackerr.Newf("Too long header in ammo at position %v", filePosition(ammoFile))
			}
			if err == io.EOF {
				break // start over from the beginning
			}
			if err != nil {
				if err == ctx.Err() {
					return err
				}
				return stackerr.Newf("error reading ammo at position: %v; error: %s", filePosition(ammoFile), err)
			}
			if len(data) == 0 {
				continue // skip empty lines
			}
			reqSize, tag, err := decodeHeader(data)
			if reqSize == 0 {
				break // start over from the beginning of file if ammo size is 0
			}
			buff := make([]byte, reqSize)
			if n, err := io.ReadFull(reader, buff); err != nil {
				if err == ctx.Err() {
					return err
				}
				return stackerr.Newf("failed to read ammo at position: %v; tried to read: %v; have read: %v; error: %s", filePosition(ammoFile), reqSize, n, err)
			}
			req, err := decodeRequest(buff)
			if err != nil {
				if err == ctx.Err() {
					return err
				}
				return stackerr.Newf("failed to decode ammo at position: %v; data: %q; error: %s", filePosition(ammoFile), buff, err)
			}
			sh := p.Pool.Get().(*simple.Ammo)
			sh.Reset(req, tag)

			select {
			case p.Sink <- sh:
				ammoNum++
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if ammoNum == 0 {
			return stackerr.Newf("no ammo in file")
		}
		if p.Passes != 0 && passNum >= p.Passes {
			break
		}
		ammoFile.Seek(0, 0)
	}
	log.Println("Ran out of ammo")
	return nil
}
