package routeros

// framer splits a RouterOS byte stream into words and sentences.
//
// It frames exactly as go-routeros's `proto/reader.go` readLength does, so a
// watcher beside the library cannot disagree with it about where a word falls.
// A value can hold anything, including the bytes of a length prefix or of a
// reply word, which is why the watchers decode rather than match bytes.
//
// It copies a word only when `keep` asks, and at most `max` bytes of it; every
// other word's bytes are counted past, not kept.
type framer struct {
	max int
	// keep decides, before a word's bytes arrive, whether to copy it: from the
	// word's position in its sentence and its length.
	keep func(index int, n int64) bool
	// onWord is told each word as it ends: the copy if it was kept, else nil.
	// The slice is reused for the next word, so a caller keeps a string of it.
	onWord func(index int, word []byte)
	// onSentence is told each sentence as its terminating empty word arrives.
	onSentence func()

	// Length prefix in progress: extra bytes still to read, and the value so far.
	lenExtra int
	lenHigh  int64
	lenShift uint
	lenAcc   int64

	// Word content in progress.
	inWord   bool
	wordLeft int64
	word     []byte
	copying  bool
	index    int
}

func (f *framer) feed(p []byte) {
	for len(p) > 0 {
		if f.inWord {
			k := f.wordLeft
			if int64(len(p)) < k {
				k = int64(len(p))
			}
			if f.copying {
				room := f.max - len(f.word)
				if room > int(k) {
					room = int(k)
				}
				if room > 0 {
					f.word = append(f.word, p[:room]...)
				}
			}
			p = p[k:]
			f.wordLeft -= k
			if f.wordLeft == 0 {
				f.inWord = false
				f.endWord()
			}
			continue
		}

		b := int64(p[0])
		p = p[1:]
		if f.lenExtra > 0 {
			f.lenAcc = f.lenAcc<<8 | b
			f.lenExtra--
			if f.lenExtra == 0 {
				f.beginWord(f.lenHigh<<f.lenShift | f.lenAcc)
			}
			continue
		}
		// The first byte of a length, decoded as proto/reader.go's readLength does.
		switch {
		case b&0x80 == 0x00:
			f.beginWord(b)
		case b&0xC0 == 0x80:
			f.startLength(b&0x3F, 1)
		case b&0xE0 == 0xC0:
			f.startLength(b&0x1F, 2)
		case b&0xF0 == 0xE0:
			f.startLength(b&0x0F, 3)
		case b&0xF8 == 0xF0:
			f.startLength(0, 4)
		default:
			// A reserved control byte. The library reads the byte itself as the
			// length, and so does this, so the two stay in step.
			f.beginWord(b)
		}
	}
}

func (f *framer) startLength(high int64, extra int) {
	f.lenHigh, f.lenExtra, f.lenShift, f.lenAcc = high, extra, uint(8*extra), 0
}

func (f *framer) beginWord(n int64) {
	if n == 0 {
		f.onSentence()
		f.index = 0
		return
	}
	f.inWord, f.wordLeft, f.word = true, n, f.word[:0]
	f.copying = f.keep(f.index, n)
}

func (f *framer) endWord() {
	var word []byte
	if f.copying {
		word = f.word
	}
	f.onWord(f.index, word)
	f.index++
}
