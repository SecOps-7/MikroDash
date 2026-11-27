/**
 * The Connections map's flow-direction choice.
 *
 * The arc is always drawn from this router's country outward. Tx follows that
 * path. Rx follows the same curve back, so the dot travels toward the router.
 * Both asks for one of each. Reversing the quadratic swaps the endpoints and
 * keeps the control point, so the line does not move when the direction does.
 */

import assert from 'node:assert';
import { flowMotions, makeArcD, reverseArcD } from '../src/pages/connections-map';

const forward = makeArcD(120, 200, 640, 180);
const back = reverseArcD(forward);

assert.notStrictEqual(back, forward, 'reversing an arc must change the path the comet follows');
assert.strictEqual(reverseArcD(back), forward, 'reversing twice must restore the arc');
assert.ok(back.startsWith('M640.0,180.0 '), 'Rx starts at the far country: ' + back);
assert.ok(back.endsWith(' 120.0,200.0'), 'Rx ends at the router: ' + back);
const control = forward.split(' Q')[1]!.split(' ')[0];
assert.ok(back.includes(' Q' + control + ' '), 'the curve keeps its control point: ' + back);

assert.deepStrictEqual(flowMotions('tx', forward).map((m) => m.dir), ['tx']);
assert.strictEqual(flowMotions('tx', forward)[0]!.path, forward, 'Tx follows the outward arc');
assert.deepStrictEqual(flowMotions('rx', forward).map((m) => m.dir), ['rx']);
assert.strictEqual(flowMotions('rx', forward)[0]!.path, back, 'Rx follows the arc back toward the router');
assert.deepStrictEqual(flowMotions('both', forward).map((m) => m.dir), ['tx', 'rx']);
assert.strictEqual(reverseArcD('not an arc'), 'not an arc');

console.log('  ok  Rx returns along the same arc, Tx leaves along it, Both draws both');
