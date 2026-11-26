package org.example;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.ArrayList;
import java.util.List;

import org.apache.flink.types.Row;
import org.apache.flink.util.Collector;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

/** Calls eval directly: the function uses neither its context nor timers. */
class EmitAfterGapTest {

    private final List<Boolean> emitted = new ArrayList<>();
    private EmitAfterGap function;
    private EmitAfterGap.EmitState state;

    @BeforeEach
    void setUp() {
        function = new EmitAfterGap();
        function.setCollector(new Collector<Boolean>() {
            @Override
            public void collect(Boolean record) {
                emitted.add(record);
            }

            @Override
            public void close() {}
        });
        state = new EmitAfterGap.EmitState();
    }

    private void eval(long gapSeconds) {
        function.eval(null, state, Row.of("checkout", "500"), gapSeconds);
    }

    @Test
    void firstRowOfAKeyIsEmitted() {
        eval(300);

        assertEquals(List.of(true), emitted);
        assertTrue(state.lastEmittedEpochMillis != null);
    }

    @Test
    void rowsInsideTheGapAreDropped() {
        eval(300);
        long first = state.lastEmittedEpochMillis;

        eval(300);
        eval(300);

        assertEquals(1, emitted.size());
        assertEquals(first, state.lastEmittedEpochMillis, "a dropped row must not extend the gap");
    }

    @Test
    void aRowAfterTheGapIsEmittedAndStartsANewGap() {
        state.lastEmittedEpochMillis = System.currentTimeMillis() - 301_000;

        eval(300);
        eval(300);

        assertEquals(1, emitted.size());
        assertTrue(System.currentTimeMillis() - state.lastEmittedEpochMillis < 10_000);
    }

    @Test
    void aRowExactlyAtTheEdgeOfTheGapIsDropped() {
        // "strictly greater than": 299 s into a 300 s gap is still inside it.
        state.lastEmittedEpochMillis = System.currentTimeMillis() - 299_000;

        eval(300);

        assertTrue(emitted.isEmpty());
    }

    @Test
    void aNullRowIsIgnored() {
        function.eval(null, state, null, 300L);

        assertTrue(emitted.isEmpty());
    }

    @Test
    void aBadGapIsRejected() {
        assertThrows(IllegalArgumentException.class, () -> function.eval(null, state, Row.of("a"), -1L));
        assertThrows(IllegalArgumentException.class, () -> function.eval(null, state, Row.of("a"), null));
    }

    @Test
    void theFunctionIsNotDeterministic() {
        assertTrue(!function.isDeterministic());
    }
}
