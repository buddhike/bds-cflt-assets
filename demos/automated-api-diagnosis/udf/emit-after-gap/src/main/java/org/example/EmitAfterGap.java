package org.example;

import static org.apache.flink.table.annotation.ArgumentTrait.PASS_COLUMNS_THROUGH;
import static org.apache.flink.table.annotation.ArgumentTrait.SCALAR;
import static org.apache.flink.table.annotation.ArgumentTrait.SET_SEMANTIC_TABLE;

import java.time.Instant;

import org.apache.flink.table.annotation.ArgumentHint;
import org.apache.flink.table.annotation.DataTypeHint;
import org.apache.flink.table.annotation.StateHint;
import org.apache.flink.table.functions.ProcessTableFunction;
import org.apache.flink.types.Row;

public class EmitAfterGap extends ProcessTableFunction<Boolean> {

    /**
     * Managed state maintained independently for each PARTITION BY key.
     */
    public static class EmitState {
        public Long lastEmittedEpochMillis = null;
    }

    /**
     * Emits the current input row when:
     *
     * 1. This is the first row for the key; or
     * 2. The current processing time (wall-clock time at which the row is
     *    processed) is strictly greater than last emitted processing time
     *    + gapSeconds.
     *
     * The Boolean output is appended to the original input columns because
     * PASS_COLUMNS_THROUGH is enabled.
     *
     * No on_time argument or watermark is required: the gap is measured
     * against the wall clock, not against a timestamp carried by the row.
     */
    public void eval(
            Context ctx,

            @StateHint(name = "state")
            EmitState state,

            @ArgumentHint(
                    value = {
                            SET_SEMANTIC_TABLE,
                            PASS_COLUMNS_THROUGH
                    },
                    name = "input")
            Row input,

            @ArgumentHint(
                    value = SCALAR,
                    name = "gapSeconds",
                    type = @DataTypeHint("BIGINT"))
            Long gapSeconds) {

        if (input == null) {
            return;
        }

        if (gapSeconds == null || gapSeconds < 0) {
            throw new IllegalArgumentException(
                    "gapSeconds must be non-null and non-negative");
        }

        Instant now = Instant.now();

        boolean shouldEmit =
                state.lastEmittedEpochMillis == null
                        || now.isAfter(
                        Instant.ofEpochMilli(
                                        state.lastEmittedEpochMillis)
                                .plusSeconds(gapSeconds));

        if (shouldEmit) {
            // PASS_COLUMNS_THROUGH preserves the input row and appends this value.
            collect(Boolean.TRUE);

            state.lastEmittedEpochMillis = now.toEpochMilli();
        }
    }

    /**
     * The result depends on the wall clock, so the function is not
     * deterministic. This also prevents the planner from constant-folding
     * calls to it.
     */
    @Override
    public boolean isDeterministic() {
        return false;
    }
}
