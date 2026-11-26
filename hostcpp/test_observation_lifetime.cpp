// Include the real host so this regression covers its shipped default too.
#define main WmpfHostEntryForTest
#include "main.cpp"
#undef main

int main() {
    if (g_obsSec != 0) {
        std::fprintf(stderr, "FAIL: default observation is %lld seconds, expected unlimited (0)\n",
                     static_cast<long long>(g_obsSec));
        return 1;
    }
    struct ParseCase { const char* value; std::int64_t expected; };
    const ParseCase cases[] = {
        {nullptr, 0}, {"", 0}, {"0", 0}, {"60", 60}, {"7200", 7200},
        {"2147483648", 2147483648LL}, {"9223372036854775807", 9223372036854775807LL},
        {"-1", 0}, {"bad", 0}, {"60s", 0}, {"9223372036854775808", 0}
    };
    for (const auto& row : cases) {
        if (ObservationSeconds(row.value) != row.expected) {
            std::fprintf(stderr, "FAIL: observation value %s\n", row.value ? row.value : "<missing>");
            return 2;
        }
    }
    const ULONGLONG began = 1000;
    if (!ObservationPending(began, began + 64000, 0) ||
        !ObservationPending(began, began + 7200000, 0) ||
        !ObservationPending(began, began + 59999, 60) ||
         ObservationPending(began, began + 60000, 60) ||
        !ObservationPending(began, began + 7200000, 2147483648LL)) {
        std::fputs("FAIL: observation deadline semantics\n", stderr);
        return 3;
    }
    std::puts("PASS: unlimited default, 11 parsing cases, finite/unlimited deadlines");
    return 0;
}
