class RingBuffer {
  constructor(maxPoints) {
    this.maxPoints = Math.max(1, maxPoints | 0);
    this.arr = new Array(this.maxPoints);
    this.start = 0;
    this.length = 0;
  }
  push(item) {
    const index = (this.start + this.length) % this.maxPoints;
    this.arr[index] = item;
    if (this.length < this.maxPoints) {
      this.length++;
      return;
    }
    this.start = (this.start + 1) % this.maxPoints;
  }
  toArray() {
    const out = new Array(this.length);
    for (let i = 0; i < this.length; i++) out[i] = this.arr[(this.start + i) % this.maxPoints];
    return out;
  }
}
module.exports = RingBuffer;
