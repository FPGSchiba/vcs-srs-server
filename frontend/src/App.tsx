import ServerControls from "./components/ServerControl";
import Header from "./components/Header";
import Footer from "./components/Footer";
import ContentWrapper from "./components/ContentWrapper";

function App() {
    return (
        <div id="App">
            <Header />
            <ServerControls />
            <ContentWrapper />
            <Footer />
        </div>
    )
}

export default App
